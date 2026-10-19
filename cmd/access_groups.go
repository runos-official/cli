package cmd

import (
	"github.com/runos-official/cli/internal/roidapi"

	"github.com/spf13/cobra"
)

var groupObject = outputFields("object", "id", "name", "idp_managed", "members")

var accessGroupsCmd = &cobra.Command{Use: "groups", Short: "List, show and change groups and their members"}

// groupRun runs one group call and prints the group it answers.
func groupRun(op string, params func(args []string) map[string]string, body func(cmd *cobra.Command, args []string) any) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		call := roidapi.Call{Op: op, Params: params(args)}
		if body != nil {
			call.Body = body(cmd, args)
		}
		if cmd.Flags().Lookup("step-up") != nil {
			call = writeCall(cmd, call)
		}
		res, err := t.do(call)
		if err != nil {
			return err
		}
		if res.StatusCode == 204 {
			return t.done("Done.")
		}
		return t.print(res, groupObject)
	}
}

func groupParam(args []string) map[string]string { return map[string]string{"group": args[0]} }

func memberParams(args []string) map[string]string {
	return map[string]string{"group": args[0], "person": args[1]}
}

var accessGroupsListCmd = &cobra.Command{
	Use: "list", Short: "List the account's groups", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		res, err := t.do(roidapi.Call{Op: "listGroups"})
		if err != nil {
			return err
		}
		return t.print(res, outputFields("array", "id", "name", "idp_managed", "members"))
	},
}

var accessGroupsShowCmd = &cobra.Command{
	Use: "show <group>", Short: "Show one group and its members", Args: cobra.ExactArgs(1),
	RunE: groupRun("getGroup", groupParam, nil),
}

var accessGroupsCreateCmd = &cobra.Command{
	Use: "create <group>", Short: "Create a group", Args: cobra.ExactArgs(1),
	RunE: groupRun("createGroup", func([]string) map[string]string { return nil }, func(cmd *cobra.Command, args []string) any {
		name, _ := cmd.Flags().GetString("name")
		managed, _ := cmd.Flags().GetBool("idp-managed")
		return map[string]any{"id": args[0], "name": name, "idp_managed": managed}
	}),
}

var accessGroupsRenameCmd = &cobra.Command{
	Use: "rename <group> <name>", Short: "Change a group's display name", Args: cobra.ExactArgs(2),
	RunE: groupRun("renameGroup", groupParam, func(_ *cobra.Command, args []string) any { return map[string]any{"name": args[1]} }),
}

var accessGroupsDeleteCmd = &cobra.Command{
	Use: "delete <group>", Short: "Delete a group that no grant uses", Args: cobra.ExactArgs(1),
	RunE: groupRun("deleteGroup", groupParam, nil),
}

var accessGroupsAddMemberCmd = &cobra.Command{
	Use: "add-member <group> <person>", Short: "Add a person to a group", Args: cobra.ExactArgs(2),
	RunE: groupRun("addGroupMember", memberParams, nil),
}

var accessGroupsRemoveMemberCmd = &cobra.Command{
	Use: "remove-member <group> <person>", Short: "Remove a person from a group", Args: cobra.ExactArgs(2),
	RunE: groupRun("removeGroupMember", memberParams, nil),
}

func init() {
	accessGroupsCreateCmd.Flags().String("name", "", "Display name")
	accessGroupsCreateCmd.Flags().Bool("idp-managed", false, "Members come from the company sign-in provider, not from these commands")
	writeFlags(accessGroupsCreateCmd, false)
	for _, c := range []*cobra.Command{accessGroupsRenameCmd, accessGroupsDeleteCmd, accessGroupsAddMemberCmd, accessGroupsRemoveMemberCmd} {
		writeFlags(c, true)
	}
	accessGroupsCmd.AddCommand(accessGroupsListCmd, accessGroupsShowCmd, accessGroupsCreateCmd, accessGroupsRenameCmd,
		accessGroupsDeleteCmd, accessGroupsAddMemberCmd, accessGroupsRemoveMemberCmd)
}
