package repomofo

import "testing"

func TestCommand(t *testing.T) {
	// "add" with no file args shows help
	{
		cmdArgs := parseCommandArgs([]string{"add", "--cli"})
		d := newDispatch(cmdArgs, SHA1HashKind)
		if _, ok := d.(dispatchHelp); !ok {
			t.Fatalf("expected dispatchHelp, got %T", d)
		}
	}

	// "add file.txt" is a valid CLI command
	{
		cmdArgs := parseCommandArgs([]string{"add", "file.txt"})
		d := newDispatch(cmdArgs, SHA1HashKind)
		cli, ok := d.(dispatchCLI)
		if !ok {
			t.Fatalf("expected dispatchCLI, got %T", d)
		}
		if cli.command.Kind != commandAdd {
			t.Fatalf("expected commandAdd, got %d", cli.command.Kind)
		}
	}

	// "commit -m" without a value shows help
	{
		cmdArgs := parseCommandArgs([]string{"commit", "-m"})
		d := newDispatch(cmdArgs, SHA1HashKind)
		if _, ok := d.(dispatchHelp); !ok {
			t.Fatalf("expected dispatchHelp, got %T", d)
		}
	}

	// "commit -m 'message'" is a valid CLI command
	{
		cmdArgs := parseCommandArgs([]string{"commit", "-m", "let there be light"})
		d := newDispatch(cmdArgs, SHA1HashKind)
		cli, ok := d.(dispatchCLI)
		if !ok {
			t.Fatalf("expected dispatchCLI, got %T", d)
		}
		if cli.command.Commit.Message != "let there be light" {
			t.Fatalf("message = %q, want %q", cli.command.Commit.Message, "let there be light")
		}
	}

	// extra config add args are joined
	{
		cmdArgs := parseCommandArgs([]string{"config", "add", "user.name", "radar", "roark"})
		d := newDispatch(cmdArgs, SHA1HashKind)
		cli, ok := d.(dispatchCLI)
		if !ok {
			t.Fatalf("expected dispatchCLI, got %T", d)
		}
		if cli.command.Config.Value != "radar roark" {
			t.Fatalf("config value = %q, want %q", cli.command.Config.Value, "radar roark")
		}
	}

	// invalid command
	{
		cmdArgs := parseCommandArgs([]string{"stats", "--clii"})
		d := newDispatch(cmdArgs, SHA1HashKind)
		inv, ok := d.(dispatchInvalidCommand)
		if !ok {
			t.Fatalf("expected dispatchInvalidCommand, got %T", d)
		}
		if inv.InvalidName != "stats" {
			t.Fatalf("invalid name = %q, want %q", inv.InvalidName, "stats")
		}
	}

	// invalid argument
	{
		cmdArgs := parseCommandArgs([]string{"status", "--clii"})
		d := newDispatch(cmdArgs, SHA1HashKind)
		inv, ok := d.(dispatchInvalidArgument)
		if !ok {
			t.Fatalf("expected dispatchInvalidArgument, got %T", d)
		}
		if inv.InvalidCmd != commandStatus {
			t.Fatalf("expected InvalidCmd to be commandStatus, got %d", inv.InvalidCmd)
		}
		if inv.InvalidName != "--clii" {
			t.Fatalf("invalid arg = %q, want %q", inv.InvalidName, "--clii")
		}
	}
}
