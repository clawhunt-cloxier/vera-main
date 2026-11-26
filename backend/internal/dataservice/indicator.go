package dataservice

// IndicatorPreparation is the registered single-stock data scope for a short study.
type IndicatorPreparation struct {
	Code        string `json:"code"`
	DatasetID   string `json:"dataset"`
	Window      int    `json:"window"`
	CloseCutoff string `json:"close_cutoff"`
}

func IndicatorWindows(dataset string) []int {
	switch dataset {
	case "mkt.daily_bar":
		return []int{1, 5, 10, 20, 60}
	case "flow.margin":
		return []int{5, 20, 60, 120}
	default:
		return nil
	}
}
