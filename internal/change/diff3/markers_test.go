package diff3

import "testing"

func TestHasMarkers(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"empty", "", false},
		{"plain", "hello\nworld\n", false},
		{"full block", "<<<<<<< ours\nX\n||||||| base\nb\n=======\nY\n>>>>>>> theirs\n", true},
		{"block without base section", "<<<<<<< ours\nX\n=======\nY\n>>>>>>> theirs\n", true},
		{"crlf block", "<<<<<<< ours\r\nX\r\n=======\r\nY\r\n>>>>>>> theirs\r\n", true},
		{"block amid content", "top\n<<<<<<< ours\nX\n=======\nY\n>>>>>>> theirs\nbottom\n", true},
		{"opener only", "<<<<<<< ours\njust talking about markers\n", false},
		{"separator only", "=======\n", false},
		{"closer only", ">>>>>>> theirs\n", false},
		{"out of order", ">>>>>>> theirs\n=======\n<<<<<<< ours\n", false},
		{"opener without space", "<<<<<<<ours\n=======\n>>>>>>> theirs\n", false},
		{"separator not line-anchored", "<<<<<<< ours\nx ======= y\n>>>>>>> theirs\n", false},
		{"markers mid-line", "see <<<<<<< ours and =======\n", false},
		{"no trailing newline", "<<<<<<< ours\nX\n=======\nY\n>>>>>>> theirs", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasMarkers([]byte(c.data)); got != c.want {
				t.Fatalf("HasMarkers(%q) = %v, want %v", c.data, got, c.want)
			}
		})
	}
}
