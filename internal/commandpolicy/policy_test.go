package commandpolicy

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestCommandRules(t *testing.T) {
	rules := []Rule{{"git push*", "ask"}, {"git push origin ariadne/*", "allow"}, {"rm -rf /*", "deny"}, {"ls*", "allow"}}
	wide := []Rule{{"git push*", "ask"}, {"timeout *", "allow"}, {"timeout 10 git push", "allow"}}
	sets := [][]Rule{rules, wide, Defaults(), {}, {{"rm -rf /*", "deny"}, {"*", "allow"}}, {{"git push*", "ask"}, {"wrap 😀 *", "allow"}}}
	commands := []string{
		"bun test 2>&1 | tail -5 &> log", "(cd x && make) > out.txt", "FOO=1 git -C . push --force", "(cd x && git push)", "env -i A=1 /usr/bin/git --no-pager -c a=b push x", "git -C repo push origin ariadne/x", "sudo rm -rf /x", `echo "$(git push)"`,
		`bash -c 'ls; git push'`, `sh -ec "rm -rf /"`, `pwsh -NoProfile -Command "git push"`, `cmd /c git push`, "if true; then git push; fi", "for i in 1; do git push; done", "{ git push; }", "! git push", "eval git push", `eval "git push"`, "echo x | xargs git push", `find . -exec git push \;`, "timeout 10 git push", "watch git push", "stdbuf -o0 git push", "source <(echo git push)", "sudo -u root git push", "env -u HOME git push", "nice -n 5 git push", "time -p git push", "command -p git push", "exec -a x git push", `bash -ce "git push"`, `bash -c -- "git push"`, `pwsh -Com "git push"`, "g`i`t push", "iex \"g`it push\"", "timeout 10 git -C repo push", "then rm -rf /", "xargs rm -rf /x", `eval "rm -rf /x"`, "timeout 5 git push", "timeout 5 make", "timeout 5 rm -rf /",
		`git commit -m "docs: explain the git push flow"`, `git log --grep "git push"`, `rg -n "git push" docs`, `cat notes.md | grep "git push"`, `echo git push`, `printf "%s\n" "git push"`, `echo "git push" | sh`, `echo "git push" | timeout 5 sh`, `echo "git push" | nice -n 5 bash`, `echo "git push" | busybox sh`, `echo git push | xargs -I{} bash -c {}`, "git commit -m x && git push", `git rebase --exec "git push" main`, `echo "rm -rf /x"`,
		"git push", "git -C . push origin +main", "cd pkg && bun publish --access public", "sudo npm publish", "npx npm publish", "pnpm publish --no-git-checks", "yarn publish", "cargo publish", "gh pr merge 12 --squash", "gh release create v1.0.0", "git stash push -m wip", `git commit -m "ci: npm publish with provenance"`, "gh pr view 12", "gh release list", "bun test", `rg "gh pr merge" .`, "ls && git push --force", "ls -la", "wrap 😀 git push", "", "# comment", "echo a # git push\ngit status", "git push # comment", "git push >out 2>&1", "git push &>>out", "git push 2>out && echo done", `'/usr/bin/git' push`, `echo 'git push\n'`, "git \\\npush", "echo $(echo $(git push))", `echo "unterminated`, `echo 'unterminated`, "echo `git push`", "echo $((git push))", "git push\u3000", "echo \"git push\n\"",
	}
	data := []byte{}
	for _, word := range utf16.Encode([]rune("git push")) {
		data = binary.LittleEndian.AppendUint16(data, word)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	for _, prefix := range []string{"-e", "-enc", "-EncodedCommand"} {
		commands = append(commands, "pwsh "+prefix+" "+encoded)
	}
	commands = append(commands, "pwsh -enc !"+encoded+"!", "pwsh -enc "+strings.TrimRight(encoded, "="))
	for _, program := range []string{"bash -c ", "eval ", "echo ", "rg -n ", "timeout 5 ", "command ", "env FOO=1 "} {
		for _, body := range []string{"git push", "ls; git push", "rm -rf /x", "gh pr merge 12", "git push origin ariadne/x"} {
			commands = append(commands, program+"'"+body+"'")
		}
	}
	type result struct {
		Parts    []string  `json:"parts"`
		Verdicts []Verdict `json:"verdicts"`
	}
	native := []result{}
	for _, command := range commands {
		parts, err := Parts(command)
		if err != nil {
			t.Fatal(command, err)
		}
		row := result{Parts: parts, Verdicts: []Verdict{}}
		for _, rules := range sets {
			row.Verdicts = append(row.Verdicts, Decide(command, rules))
		}
		native = append(native, row)
	}
	for _, command := range []string{"git push", "sudo git push", `bash -c 'git push'`, "g`i`t push"} {
		if verdict := Decide(command, Defaults()); verdict.Action != "ask" {
			t.Fatal("push approval bypassed", command, verdict)
		}
	}
	if len(native) != len(commands) {
		t.Fatal("result count differs")
	}
}
func TestPolicyBoundsComplexityAndRetainsDenyPriority(t *testing.T) {
	complex := "echo " + strings.Repeat("x ", 16000)
	if verdict := Decide(complex, []Rule{{"*", "allow"}}); verdict.Action != "ask" {
		t.Fatal("incomplete examination allowed command", verdict)
	}
	if verdict := Decide(strings.Repeat("x", 33<<10), nil); verdict.Action != "ask" {
		t.Fatal("oversized command allowed")
	}
	if verdict := Decide("git push; rm -rf /x", []Rule{{"git push*", "ask"}, {"*", "allow"}, {"rm -rf /*", "deny"}}); verdict.Action != "deny" {
		t.Fatal("allow lifted deny")
	}
}
func TestGlobTreatsEverythingExceptStarLiterally(t *testing.T) {
	for _, literal := range []string{"[x]", "a.b", "x?", "a+b", "(cmd)", "$HOME", "a^b", "a|b", "a\\b"} {
		if !glob(literal, literal) || glob(literal, "other") {
			t.Fatal("pattern treated as shell or regular expression", literal)
		}
	}
}
