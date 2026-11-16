package resultfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestResultFileRoundTripAndRanges(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		for _, size := range []int{0, 1, BlockSize - 1, BlockSize, 2*BlockSize + 127} {
			data := bytes.Repeat([]byte("中文-result\n"), size/14+1)[:size]
			path := filepath.Join(t.TempDir(), "payload")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := Write(context.Background(), f, bytes.NewReader(data), compressed)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := Open(context.Background(), path, meta)
			if err != nil {
				t.Fatal(err)
			}
			for _, off := range []int64{0, int64(size / 2), BlockSize - 17, int64(size)} {
				buf := make([]byte, 73)
				n, readErr := r.ReadAt(buf, off)
				want := 0
				if off < int64(size) {
					want = min(73, size-int(off))
				}
				if n != want || (n < 73 && !errors.Is(readErr, io.EOF)) || (n == 73 && readErr != nil) {
					t.Fatalf("compressed=%t size=%d offset=%d n=%d err=%v", compressed, size, off, n, readErr)
				}
				if want > 0 && !bytes.Equal(buf[:n], data[int(off):int(off)+want]) {
					t.Fatal("range differs from original")
				}
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResultFileDetectsCorruptionAndCancellation(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "payload")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := Write(context.Background(), f, bytes.NewReader(bytes.Repeat([]byte("abc"), BlockSize)), compressed)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		r, err := Open(ctx, path, meta)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if _, err = r.ReadAt(make([]byte, 10), 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
		if err = r.Close(); err != nil {
			t.Fatal(err)
		}
		f, err = os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.WriteAt([]byte{0xff}, 9); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		r, err = Open(context.Background(), path, meta)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.ReadAt(make([]byte, 10), 0); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corrupt=%v", err)
		}
		if err = r.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
