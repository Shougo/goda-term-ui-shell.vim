package shell

import (
	"regexp"
	"testing"
)

func TestDefaultPasswordPattern(t *testing.T) {
	re := regexp.MustCompile(DefaultParams().PasswordPattern)
	for _, s := range []string{
		"[sudo] password for shougo: ",
		"[sudo: authenticate] Password: ", // sudo-rs
		"Enter passphrase for key '/home/shougo/.ssh/id_ed25519': ",
		"Enter password: ",
		"Old Password: ",
		"New password: ",
		"Repeat password: ",
		"UNIX password: ",
	} {
		if !re.MatchString(s) {
			t.Errorf("%q is not a password prompt", s)
		}
	}
	for _, s := range []string{
		"cat password.txt",
		"passwd: password updated successfully",
		"[sudo] is not a prompt",
		"sudo password",
	} {
		if re.MatchString(s) {
			t.Errorf("%q is a password prompt", s)
		}
	}
}
