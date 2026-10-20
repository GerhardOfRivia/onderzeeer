package commandparse

import (
	"reflect"
	"testing"
)

func TestTokenizeLiteralArguments(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{`tool 'a b' escaped\ space "" '{{file}}' '$HOME'`, []string{"tool", "a b", "escaped space", "", "{{file}}", "$HOME"}},
		{"docker run \\\n --rm alpine echo hi", []string{"docker", "run", "--rm", "alpine", "echo", "hi"}},
		{`tool "a\qb" "escaped\"quote"`, []string{"tool", `a\qb`, `escaped"quote`}},
	} {
		got, err := Tokenize(test.input)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("%q: %q %v", test.input, got, err)
		}
	}
}
func TestTokenizeRejectsAmbiguousShell(t *testing.T) {
	for _, input := range []string{"", "echo 'oops", "echo x\\", "echo hi | cat", "echo $(id)", "echo $HOME", "echo `id`", "echo hi > output", "echo x\necho y", "A=b command", "echo a && echo b"} {
		if _, err := Tokenize(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
