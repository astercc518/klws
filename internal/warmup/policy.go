package warmup

type Lane string

const (
	LaneFast     Lane = "FAST"
	LaneStandard Lane = "STANDARD"
)

// Policy 是一条车道的养号阈值与配额上限,存 warmup_policies,可后台热改。
type Policy struct {
	MinWarmupMessages int
	MinReplies        int
	MinOnlineHours    int
	WarmingCap        int
	MatureBaseCap     int
	MatureMaxCap      int
	MatureRampStep    int
}

// MeetsPromotion 判断信号是否够 WARMING->MATURE(三条全达标)。
func MeetsPromotion(msgs, replies int, onlineHours float64, p Policy) bool {
	return msgs >= p.MinWarmupMessages &&
		replies >= p.MinReplies &&
		onlineHours >= float64(p.MinOnlineHours)
}

// DailyCap 返回该阶段当日允许的发送额。NEW=0;WARMING=warmingCap;
// MATURE=base+matureDays*step,封顶 max。
func DailyCap(p Policy, stage Stage, matureDays int) int {
	switch stage {
	case StageNew:
		return 0
	case StageWarming:
		return p.WarmingCap
	default:
		cap := p.MatureBaseCap + matureDays*p.MatureRampStep
		if cap > p.MatureMaxCap {
			return p.MatureMaxCap
		}
		return cap
	}
}
