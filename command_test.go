package repomofo

import "testing"

func TestCommand(t *testing.T) {
	// "add" with no file args shows help
	{
		cmdArgs := ParseCommandArgs([]string{"add", "--cli"})
		dispatch := NewDispatch(cmdArgs)
		if dispatch.Kind != DispatchHelp {
			t.Fatalf("expected DispatchHelp, got %d", dispatch.Kind)
		}
	}

	// "add file.txt" is a valid CLI command
	{
		cmdArgs := ParseCommandArgs([]string{"add", "file.txt"})
		dispatch := NewDispatch(cmdArgs)
		if dispatch.Kind != DispatchCLI {
			t.Fatalf("expected DispatchCLI, got %d", dispatch.Kind)
		}
		if dispatch.Command.Kind != CommandAdd {
			t.Fatalf("expected CommandAdd, got %d", dispatch.Command.Kind)
		}
	}

	// "commit -m" without a value shows help
	{
		cmdArgs := ParseCommandArgs([]string{"commit", "-m"})
		dispatch := NewDispatch(cmdArgs)
		if dispatch.Kind != DispatchHelp {
			t.Fatalf("expected DispatchHelp, got %d", dispatch.Kind)
		}
	}

	// "commit -m 'message'" is a valid CLI command
	{
		cmdArgs := ParseCommandArgs([]string{"commit", "-m", "let there be light"})
		dispatch := NewDispatch(cmdArgs)
		if dispatch.Kind != DispatchCLI {
			t.Fatalf("expected DispatchCLI, got %d", dispatch.Kind)
		}
		if dispatch.Command.Commit.Message != "let there be light" {
			t.Fatalf("message = %q, want %q", dispatch.Command.Commit.Message, "let there be light")
		}
	}

	// extra config add args are joined
	{
		cmdArgs := ParseCommandArgs([]string{"config", "add", "user.name", "radar", "roark"})
		dispatch := NewDispatch(cmdArgs)
		if dispatch.Kind != DispatchCLI {
			t.Fatalf("expected DispatchCLI, got %d", dispatch.Kind)
		}
		if dispatch.Command.Config.Value != "radar roark" {
			t.Fatalf("config value = %q, want %q", dispatch.Command.Config.Value, "radar roark")
		}
	}

	// invalid command
	{
		cmdArgs := ParseCommandArgs([]string{"stats", "--clii"})
		dispatch := NewDispatch(cmdArgs)
		if dispatch.Kind != DispatchInvalidCommand {
			t.Fatalf("expected DispatchInvalidCommand, got %d", dispatch.Kind)
		}
		if dispatch.InvalidName != "stats" {
			t.Fatalf("invalid name = %q, want %q", dispatch.InvalidName, "stats")
		}
	}

	// invalid argument
	{
		cmdArgs := ParseCommandArgs([]string{"status", "--clii"})
		dispatch := NewDispatch(cmdArgs)
		if dispatch.Kind != DispatchInvalidArgument {
			t.Fatalf("expected DispatchInvalidArgument, got %d", dispatch.Kind)
		}
		if dispatch.InvalidCmd == nil || *dispatch.InvalidCmd != CommandStatus {
			t.Fatal("expected InvalidCmd to be CommandStatus")
		}
		if dispatch.InvalidName != "--clii" {
			t.Fatalf("invalid arg = %q, want %q", dispatch.InvalidName, "--clii")
		}
	}
}
