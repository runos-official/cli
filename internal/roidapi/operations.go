package roidapi

// operation is one route of the access API, named by its OpenAPI
// operationId (testdata/rbac.yaml; ops_contract_test.go keeps the two in
// step). etagFrom names the read whose ETag a change must send as
// If-Match.
type operation struct {
	method   string
	path     string
	etagFrom string
}

const (
	accessBase = "/v1/accounts/{acct}/access"
	roleItem   = accessBase + "/roles/{role}"
	groupItem  = accessBase + "/groups/{group}"
	grantItem  = accessBase + "/grants/{grant}"
)

var operations = map[string]operation{
	"listActions":        {method: "GET", path: accessBase + "/actions"},
	"listRoles":          {method: "GET", path: accessBase + "/roles"},
	"createRole":         {method: "POST", path: accessBase + "/roles"},
	"getRole":            {method: "GET", path: roleItem},
	"updateRole":         {method: "PUT", path: roleItem, etagFrom: "getRole"},
	"deleteRole":         {method: "DELETE", path: roleItem, etagFrom: "getRole"},
	"listGroups":         {method: "GET", path: accessBase + "/groups"},
	"createGroup":        {method: "POST", path: accessBase + "/groups"},
	"getGroup":           {method: "GET", path: groupItem},
	"renameGroup":        {method: "PATCH", path: groupItem, etagFrom: "getGroup"},
	"deleteGroup":        {method: "DELETE", path: groupItem, etagFrom: "getGroup"},
	"addGroupMember":     {method: "PUT", path: groupItem + "/members/{person}", etagFrom: "getGroup"},
	"removeGroupMember":  {method: "DELETE", path: groupItem + "/members/{person}", etagFrom: "getGroup"},
	"listGrants":         {method: "GET", path: accessBase + "/grants"},
	"createGrant":        {method: "POST", path: accessBase + "/grants"},
	"getGrant":           {method: "GET", path: grantItem},
	"revokeGrant":        {method: "DELETE", path: grantItem, etagFrom: "getGrant"},
	"getGrantAudit":      {method: "GET", path: grantItem + "/audit"},
	"getEffectiveAccess": {method: "GET", path: accessBase + "/effective"},
}
