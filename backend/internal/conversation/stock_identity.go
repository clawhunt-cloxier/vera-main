package conversation

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

var parentheticalStockCode = regexp.MustCompile(`[（(][[:space:]　]*([0-9]{6}(?:\.[a-z]{2,8})?|[a-z][a-z0-9]{0,14}[0-9]\.[a-z]{2,8})[[:space:]　]*[）)]`)

type stockIdentityGroup struct {
	subjectSpan
	name, code string
}

// A code in parentheses qualifies the adjacent catalog name. Keep the full
// declaration as one occurrence so the same role applies to both aliases.
func groupStockIdentities(question string, spans []subjectSpan) ([]stockIdentityGroup, bool) {
	var groups []stockIdentityGroup
	conflicted := false
	for _, match := range parentheticalStockCode.FindAllStringSubmatchIndex(question, -1) {
		nameEnd := len(strings.TrimRightFunc(question[:match[0]], unicode.IsSpace))
		nameStart := nameEnd
		for _, span := range spans {
			if span.end == nameEnd && span.start < nameStart {
				nameStart = span.start
			}
		}
		if nameStart == nameEnd {
			continue
		}
		var ids []uuid.UUID
		for _, name := range spans {
			if name.start != nameStart || name.end != nameEnd {
				continue
			}
			for _, code := range spans {
				if code.start == match[2] && code.end == match[3] && code.id == name.id && !slices.Contains(ids, code.id) {
					ids = append(ids, code.id)
				}
			}
		}
		if len(ids) != 1 {
			// A known name cannot stand in for an unknown or conflicting code.
			conflicted = true
			continue
		}
		groups = append(groups, stockIdentityGroup{
			subjectSpan: subjectSpan{id: ids[0], start: nameStart, end: match[1]},
			name:        question[nameStart:nameEnd],
			code:        question[match[2]:match[3]],
		})
	}
	return groups, conflicted
}

func (roles stockSubjectRoles) normalizeMentions(question string, mentions []string) []string {
	var targets []string
	for _, raw := range mentions {
		mention := strings.TrimSpace(raw)
		if roles.nonTargetMention(question, mention) {
			continue
		}
		normalized := strings.ToLower(mention)
		if match := parentheticalStockCode.FindStringSubmatchIndex(normalized); len(match) >= 4 && match[1] == len(normalized) {
			name := strings.TrimSpace(normalized[:match[0]])
			code := normalized[match[2]:match[3]]
			var group *stockIdentityGroup
			for i := range roles.groups {
				observed := &roles.groups[i]
				if observed.name != name || observed.code != code {
					continue
				}
				group = observed
				if slices.Contains(roles.possible, observed.subjectSpan) {
					break
				}
			}
			if group != nil {
				// The model may normalize punctuation. The matching group was
				// already verified in the source against the catalog.
				if slices.Contains(roles.possible, group.subjectSpan) {
					targets = append(targets, group.code)
				}
				continue
			}
		}
		targets = append(targets, mention)
	}
	return targets
}
