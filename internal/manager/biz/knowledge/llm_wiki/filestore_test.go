package llm_wiki

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureRemovesLegacyConceptAndEntityDirectories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	files, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, files.Ensure(ctx))

	for _, relative := range []string{"concepts", "entities", "wiki/concepts", "wiki/entities"} {
		dir := filepath.Join(root, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.md"), []byte("legacy"), 0o640))
	}

	require.NoError(t, files.Ensure(ctx))

	for _, relative := range []string{"concepts", "entities", "wiki/concepts", "wiki/entities"} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}
