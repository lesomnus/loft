package domain

import "strings"

// workspaceWords is a workspace's name as a sentence says it: "<이름> 워크스페이스",
// unless the name says so already.
func workspaceWords(name string) string {
	if strings.HasSuffix(name, "워크스페이스") {
		return name
	}
	return name + " 워크스페이스"
}
