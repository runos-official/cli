package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/runos-official/cli/internal/roidapi"

	"github.com/spf13/cobra"
)

var roleFields = outputFields("array", "id", "name", "builtin", "version", "actions")

var accessRolesCmd = &cobra.Command{Use: "roles", Short: "List, show and change roles"}

var accessRolesListCmd = &cobra.Command{
	Use: "list", Short: "List the account's roles, built-in and custom", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		res, err := t.do(roidapi.Call{Op: "listRoles"})
		if err != nil {
			return err
		}
		return t.print(res, roleFields)
	},
}

var accessRolesShowCmd = &cobra.Command{
	Use: "show <role>", Short: "Show one role and the actions it holds", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		res, err := t.do(roidapi.Call{Op: "getRole", Params: map[string]string{"role": args[0]}})
		if err != nil {
			return err
		}
		return t.print(res, outputFields("object", "id", "name", "builtin", "version", "actions"))
	},
}

var accessRolesCreateCmd = &cobra.Command{
	Use: "create <role>", Short: "Create a custom role", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		actions, _ := cmd.Flags().GetStringSlice("action")
		if name == "" {
			name = args[0]
		}
		res, err := t.do(writeCall(cmd, roidapi.Call{Op: "createRole", Body: map[string]any{"id": args[0], "name": name, "actions": actions}}))
		if err != nil {
			return err
		}
		return t.print(res, outputFields("object", "id", "name", "actions"))
	},
}

var accessRolesUpdateCmd = &cobra.Command{
	Use: "update <role>", Short: "Rename a custom role or replace its actions", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		params := map[string]string{"role": args[0]}
		// PUT replaces the whole role: start from the version just read and
		// send its ETag, so a concurrent edit makes this change fail, not vanish.
		cur, err := t.do(roidapi.Call{Op: "getRole", Params: params})
		if err != nil {
			return err
		}
		var role struct {
			Name    string   `json:"name"`
			Actions []string `json:"actions"`
		}
		if err := json.Unmarshal(cur.Body, &role); err != nil {
			return fmt.Errorf("could not read the role: %w", err)
		}
		if cmd.Flags().Changed("name") {
			role.Name, _ = cmd.Flags().GetString("name")
		}
		if cmd.Flags().Changed("action") {
			role.Actions, _ = cmd.Flags().GetStringSlice("action")
		}
		call := writeCall(cmd, roidapi.Call{Op: "updateRole", Params: params, Body: map[string]any{"name": role.Name, "actions": role.Actions}})
		if call.IfMatch == "" {
			call.IfMatch = cur.Header.Get("ETag")
		}
		res, err := t.do(call)
		if err != nil {
			return err
		}
		return t.print(res, outputFields("object", "id", "name", "actions"))
	},
}

var accessRolesDeleteCmd = &cobra.Command{
	Use: "delete <role>", Short: "Delete a custom role that no grant uses", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		if _, err := t.do(writeCall(cmd, roidapi.Call{Op: "deleteRole", Params: map[string]string{"role": args[0]}})); err != nil {
			return err
		}
		return t.done("Deleted role " + args[0] + ".")
	},
}

func init() {
	accessRolesCreateCmd.Flags().String("name", "", "Display name (default: the role id)")
	accessRolesCreateCmd.Flags().StringSlice("action", nil, "An action id or prefix pattern such as clusters.* (repeatable)")
	_ = accessRolesCreateCmd.MarkFlagRequired("action")
	writeFlags(accessRolesCreateCmd, false)
	accessRolesUpdateCmd.Flags().String("name", "", "New display name")
	accessRolesUpdateCmd.Flags().StringSlice("action", nil, "The full new list of actions (repeatable)")
	writeFlags(accessRolesUpdateCmd, true)
	writeFlags(accessRolesDeleteCmd, true)
	accessRolesCmd.AddCommand(accessRolesListCmd, accessRolesShowCmd, accessRolesCreateCmd, accessRolesUpdateCmd, accessRolesDeleteCmd)
}
