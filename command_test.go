package repomofo

import "testing"

func TestCommand(t *testing.T) {
	// "add" with no file args shows help
	{
		cmdArgs := parseCommandArgs([]string{"add", "--cli"})
		dispatch := newDispatch(cmdArgs)
		if dispatch.Kind != dispatchHelp {
			t.Fatalf("expected dispatchHelp, got %d", dispatch.Kind)
		}
	}

	// "add file.txt" is a valid CLI command
	{
		cmdArgs := parseCommandArgs([]string{"add", "file.txt"})
		dispatch := newDispatch(cmdArgs)
		if dispatch.Kind != dispatchCLI {
			t.Fatalf("expected dispatchCLI, got %d", dispatch.Kind)
		}
		if dispatch.command.Kind != commandAdd {
			t.Fatalf("expected commandAdd, got %d", dispatch.command.Kind)
		}
	}

	// "commit -m" without a value shows help
	{
		cmdArgs := parseCommandArgs([]string{"commit", "-m"})
		dispatch := newDispatch(cmdArgs)
		if dispatch.Kind != dispatchHelp {
			t.Fatalf("expected dispatchHelp, got %d", dispatch.Kind)
		}
	}

	// "commit -m 'message'" is a valid CLI command
	{
		cmdArgs := parseCommandArgs([]string{"commit", "-m", "let there be light"})
		dispatch := newDispatch(cmdArgs)
		if dispatch.Kind != dispatchCLI {
			t.Fatalf("expected dispatchCLI, got %d", dispatch.Kind)
		}
		if dispatch.command.Commit.Message != "let there be light" {
			t.Fatalf("message = %q, want %q", dispatch.command.Commit.Message, "let there be light")
		}
	}

	// extra config add args are joined
	{
		cmdArgs := parseCommandArgs([]string{"config", "add", "user.name", "radar", "roark"})
		dispatch := newDispatch(cmdArgs)
		if dispatch.Kind != dispatchCLI {
			t.Fatalf("expected dispatchCLI, got %d", dispatch.Kind)
		}
		if dispatch.command.Config.Value != "radar roark" {
			t.Fatalf("config value = %q, want %q", dispatch.command.Config.Value, "radar roark")
		}
	}

	// invalid command
	{
		cmdArgs := parseCommandArgs([]string{"stats", "--clii"})
		dispatch := newDispatch(cmdArgs)
		if dispatch.Kind != dispatchInvalidCommand {
			t.Fatalf("expected dispatchInvalidCommand, got %d", dispatch.Kind)
		}
		if dispatch.InvalidName != "stats" {
			t.Fatalf("invalid name = %q, want %q", dispatch.InvalidName, "stats")
		}
	}

	// invalid argument
	{
		cmdArgs := parseCommandArgs([]string{"status", "--clii"})
		dispatch := newDispatch(cmdArgs)
		if dispatch.Kind != dispatchInvalidArgument {
			t.Fatalf("expected dispatchInvalidArgument, got %d", dispatch.Kind)
		}
		if dispatch.InvalidCmd == nil || *dispatch.InvalidCmd != commandStatus {
			t.Fatal("expected InvalidCmd to be commandStatus")
		}
		if dispatch.InvalidName != "--clii" {
			t.Fatalf("invalid arg = %q, want %q", dispatch.InvalidName, "--clii")
		}
	}
}
