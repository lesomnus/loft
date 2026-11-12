package domain

import "strings"

// roleWords is what each role is called, as a person reads it.
var roleWords = map[string]string{"owner": "소유자", "admin": "관리자", "manager": "매니저", "member": "구성원", "auditor": "감사자"}

// workspaceWords is a workspace's name as a sentence says it: "<이름> 워크스페이스",
// unless the name says so already.
func workspaceWords(name string) string {
	if strings.HasSuffix(name, "워크스페이스") {
		return name
	}
	return name + " 워크스페이스"
}
