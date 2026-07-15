// Package warmup 实现新号养号子系统:状态机、策略、服务、脚本库。
package warmup

import "errors"

type Stage string
type Event string

const (
	StageNew     Stage = "NEW"
	StageWarming Stage = "WARMING"
	StageMature  Stage = "MATURE"

	EventEnroll  Event = "ENROLL"
	EventPromote Event = "PROMOTE"
	EventDemote  Event = "DEMOTE"
)

// ErrInvalidTransition 表示当前阶段不接受该事件。
var ErrInvalidTransition = errors.New("warmup: invalid stage transition")

var transitions = map[Stage]map[Event]Stage{
	StageNew:     {EventEnroll: StageWarming},
	StageWarming: {EventPromote: StageMature},
	StageMature:  {EventDemote: StageWarming},
}

// Transition 返回 s 接受 e 后的新阶段;非法组合返回 ErrInvalidTransition。
func Transition(s Stage, e Event) (Stage, error) {
	if next, ok := transitions[s][e]; ok {
		return next, nil
	}
	return "", ErrInvalidTransition
}
