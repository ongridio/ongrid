// Package resultfile stores immutable tool output as plain bytes or independently
// compressed Zstandard frames using the public Zstandard Seekable Format.
package resultfile

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

const (
	BlockSize         = 1 << 20
	MaxSize           = 64 << 20
	Plain             = "none"
	Seekable          = "zstd_seekable"
	seekMagic  uint32 = 0x8f92eab1
	tableMagic uint32 = 0x184d2a5e
)

var ErrCorrupt = errors.New("tool result payload is corrupt")

type Metadata struct {
	Encoding    string   `json:"encoding"`
	Size        int64    `json:"size"`
	StoredSize  int64    `json:"stored_size"`
	SHA256      string   `json:"sha256"`
	BlockHashes []string `json:"block_hashes"`
}

// Write never closes dst. The caller must sync and close it before publication.
func Write(ctx context.Context, dst io.Writer, src io.Reader, compressed bool) (Metadata, error) {
	m := Metadata{Encoding: Plain}
	var encoder *zstd.Encoder
	if compressed {
		m.Encoding = Seekable
		var err error
		encoder, err = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(BlockSize), zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(true))
		if err != nil {
			return m, fmt.Errorf("create result encoder: %w", err)
		}
		defer encoder.Close()
	}
	hash := sha256.New()
	buffer := make([]byte, BlockSize)
	var entries []uint32
	for {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		n, err := io.ReadFull(&contextReader{ctx, src}, buffer)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return m, fmt.Errorf("read result: %w", err)
		}
		if n == 0 {
			break
		}
		if m.Size+int64(n) > MaxSize {
			return m, fmt.Errorf("tool result exceeds %d bytes", MaxSize)
		}
		block := buffer[:n]
		hash.Write(block) // hash.Hash.Write cannot fail.
		digest := sha256.Sum256(block)
		m.BlockHashes = append(m.BlockHashes, hex.EncodeToString(digest[:]))
		payload := block
		if encoder != nil {
			payload = encoder.EncodeAll(block, nil)
		}
		if err := writeAll(dst, payload); err != nil {
			return m, fmt.Errorf("write result block: %w", err)
		}
		entries = append(entries, uint32(len(payload)), uint32(n))
		m.Size += int64(n)
		m.StoredSize += int64(len(payload))
	}
	if encoder != nil {
		// A skippable frame contains entries followed by the nine-byte footer.
		table := make([]byte, 8+4*len(entries)+9)
		binary.LittleEndian.PutUint32(table, tableMagic)
		binary.LittleEndian.PutUint32(table[4:], uint32(len(table)-8))
		for i, size := range entries {
			binary.LittleEndian.PutUint32(table[8+i*4:], size)
		}
		footer := table[len(table)-9:]
		binary.LittleEndian.PutUint32(footer, uint32(len(entries)/2))
		binary.LittleEndian.PutUint32(footer[5:], seekMagic)
		if err := writeAll(dst, table); err != nil {
			return m, fmt.Errorf("write result seek table: %w", err)
		}
		m.StoredSize += int64(len(table))
	}
	m.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return m, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func writeAll(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}

type frame struct {
	physical, logical int64
	compressed, raw   int
}

// Reader checks each requested block against the immutable manifest. It keeps
// only one decoded block, so sequential scans do not repeatedly decode a frame.
// A Reader belongs to one request and is not safe for concurrent use.
type Reader struct {
	ctx     context.Context
	file    *os.File
	meta    Metadata
	frames  []frame
	block   int
	decoded []byte
	decoder *zstd.Decoder
}

func Open(ctx context.Context, path string, meta Metadata) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if meta.Size < 0 || meta.Size > MaxSize || meta.StoredSize < 0 || len(meta.BlockHashes) != int((meta.Size+BlockSize-1)/BlockSize) {
		return nil, ErrCorrupt
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open result payload: %w", err)
	}
	r := &Reader{ctx: ctx, file: f, meta: meta, block: -1}
	if err := r.initialize(); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return r, nil
}

func (r *Reader) initialize() error {
	stat, err := r.file.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() != r.meta.StoredSize {
		return ErrCorrupt
	}
	if r.meta.Encoding == Plain {
		if r.meta.StoredSize != r.meta.Size {
			return ErrCorrupt
		}
		for offset := int64(0); offset < r.meta.Size; offset += BlockSize {
			n := int(min(int64(BlockSize), r.meta.Size-offset))
			r.frames = append(r.frames, frame{offset, offset, n, n})
		}
		return nil
	}
	if r.meta.Encoding != Seekable || stat.Size() < 17 {
		return ErrCorrupt
	}
	footer := make([]byte, 9)
	if _, err := r.file.ReadAt(footer, stat.Size()-9); err != nil {
		return err
	}
	n := int(binary.LittleEndian.Uint32(footer))
	if n != len(r.meta.BlockHashes) || footer[4] != 0 || binary.LittleEndian.Uint32(footer[5:]) != seekMagic {
		return ErrCorrupt
	}
	tableSize := int64(8 + 8*n + 9)
	if tableSize > stat.Size() {
		return ErrCorrupt
	}
	table := make([]byte, tableSize)
	if _, err := r.file.ReadAt(table, stat.Size()-tableSize); err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(table) != tableMagic || int64(binary.LittleEndian.Uint32(table[4:])) != tableSize-8 {
		return ErrCorrupt
	}
	var physical, logical int64
	for i := 0; i < n; i++ {
		compressed := int(binary.LittleEndian.Uint32(table[8+8*i:]))
		raw := int(binary.LittleEndian.Uint32(table[12+8*i:]))
		if compressed < 1 || compressed > BlockSize+BlockSize/8 || raw != int(min(int64(BlockSize), r.meta.Size-logical)) || raw <= 0 {
			return ErrCorrupt
		}
		r.frames = append(r.frames, frame{physical, logical, compressed, raw})
		physical += int64(compressed)
		logical += int64(raw)
	}
	if physical != stat.Size()-tableSize || logical != r.meta.Size {
		return ErrCorrupt
	}
	r.decoder, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(2*BlockSize), zstd.WithDecoderMaxWindow(BlockSize), zstd.WithDecodeAllCapLimit(true))
	return err
}

func (r *Reader) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative result offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if offset >= r.meta.Size {
		return 0, io.EOF
	}
	written := 0
	for written < len(p) && offset < r.meta.Size {
		if err := r.ctx.Err(); err != nil {
			return written, err
		}
		block := int(offset / BlockSize)
		if block != r.block {
			f := r.frames[block]
			encoded := make([]byte, f.compressed)
			if _, err := r.file.ReadAt(encoded, f.physical); err != nil {
				return written, err
			}
			decoded := encoded
			if r.decoder != nil {
				var err error
				decoded, err = r.decoder.DecodeAll(encoded, make([]byte, 0, f.raw))
				if err != nil {
					return written, fmt.Errorf("%w: decode block: %v", ErrCorrupt, err)
				}
			}
			digest := sha256.Sum256(decoded)
			if len(decoded) != f.raw || hex.EncodeToString(digest[:]) != r.meta.BlockHashes[block] {
				return written, ErrCorrupt
			}
			r.decoded, r.block = decoded, block
		}
		n := copy(p[written:], r.decoded[offset-r.frames[block].logical:])
		written += n
		offset += int64(n)
	}
	if written < len(p) {
		return written, io.EOF
	}
	return written, nil
}

func (r *Reader) Close() error {
	if r.decoder != nil {
		r.decoder.Close()
	}
	return r.file.Close()
}
