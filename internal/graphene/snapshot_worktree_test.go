package graphene

import (
	"fmt"
	"testing"
)

func TestSnapshotPathCollision(t *testing.T) {
	owned := snapshotPathTree{}
	for _, path := range []string{"a-file", "dir.txt", "dir/file", "file", "z/file", "dir/sub/file"} {
		owned.add(path)
	}
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"file", false},
		{"dir/file", false},
		{"file/precious", true},
		{"file/nested/precious", true},
		{"dir", true},
		{"z", true},
		{"dir/other", false},
		{"dir/sub/file", false},
		{"dir/sub", true},
		{"dir/sub/other", false},
		{"dir/sub/file/precious", true},
		{"fi", false},
		{"file.txt", false},
		{"node_modules/package/index.js", false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			if got := owned.collides(tt.path); got != tt.want {
				t.Fatalf("collision = %v, want %v", got, tt.want)
			}
		})
	}
}

func BenchmarkSnapshotPathCollision(b *testing.B) {
	owned := snapshotPathTree{}
	for i := range 20000 {
		owned.add(fmt.Sprintf("src/package-%05d/index.ts", i))
	}
	b.ResetTimer()
	for b.Loop() {
		if owned.collides("node_modules/package/lib/index.js") {
			b.Fatal("unexpected collision")
		}
	}
}
