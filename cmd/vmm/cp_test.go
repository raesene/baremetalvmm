package main

import "testing"

func TestParseCopyArgs(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		dest    string
		want    copySpec
		wantErr bool
	}{
		{
			name: "host to vm",
			src:  "./app.tar.gz",
			dest: "myvm:/tmp/",
			want: copySpec{vmName: "myvm", guestPath: "/tmp/", hostPath: "./app.tar.gz", toVM: true},
		},
		{
			name: "vm to host",
			src:  "myvm:/var/log/syslog",
			dest: "syslog",
			want: copySpec{vmName: "myvm", guestPath: "/var/log/syslog", hostPath: "syslog"},
		},
		{
			name: "empty guest path means home",
			src:  "notes.txt",
			dest: "myvm:",
			want: copySpec{vmName: "myvm", guestPath: "", hostPath: "notes.txt", toVM: true},
		},
		{
			name: "guest path containing colons",
			src:  "myvm:/tmp/a:b",
			dest: ".",
			want: copySpec{vmName: "myvm", guestPath: "/tmp/a:b", hostPath: "."},
		},
		{
			name: "relative host path with colon stays local",
			src:  "./myvm:x",
			dest: "myvm:/tmp/",
			want: copySpec{vmName: "myvm", guestPath: "/tmp/", hostPath: "./myvm:x", toVM: true},
		},
		{
			name: "absolute host path with colon stays local",
			src:  "myvm:/etc/hosts",
			dest: "/tmp/a:b",
			want: copySpec{vmName: "myvm", guestPath: "/etc/hosts", hostPath: "/tmp/a:b"},
		},
		{name: "both local", src: "a", dest: "b", wantErr: true},
		{name: "both remote", src: "vm1:/a", dest: "vm2:/b", wantErr: true},
		{name: "invalid vm name", src: "a", dest: "bad$name:/tmp", wantErr: true},
		{name: "dot vm name", src: "..:/etc/passwd", dest: "x", wantErr: true},
		{name: "empty host path", src: "", dest: "myvm:/tmp", wantErr: true},
		{name: "leading colon is local", src: ":foo", dest: "bar", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCopyArgs(tt.src, tt.dest)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseCopyArgs(%q, %q) = %+v, want error", tt.src, tt.dest, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCopyArgs(%q, %q) unexpected error: %v", tt.src, tt.dest, err)
			}
			if got != tt.want {
				t.Errorf("parseCopyArgs(%q, %q) = %+v, want %+v", tt.src, tt.dest, got, tt.want)
			}
		})
	}
}
