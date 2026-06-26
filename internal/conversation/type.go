package conversation

import "strings"

const (
	TypeDirect = "direct"
	TypeGroup  = "group"
	TypeThread = "thread"
)

func NormalizeType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "group":
		return TypeGroup
	case "thread", "topic", "topic_group":
		return TypeThread
	case "direct", "p2p", "":
		return TypeDirect
	default:
		return TypeDirect
	}
}
