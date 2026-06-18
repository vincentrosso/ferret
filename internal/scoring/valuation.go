package scoring

// MaxBid returns the maximum auction bid to achieve targetROI (e.g. 0.50 for 50%)
// given the estimated resale and mid-point repair cost.
// Formula: bid = resale / (1 + targetROI) - repairMid
func MaxBid(resale, repairMid int, targetROI float64) int {
	if resale == 0 || targetROI <= 0 {
		return 0
	}
	mb := int(float64(resale)/(1+targetROI)) - repairMid
	if mb < 0 {
		return 0
	}
	return mb
}

// ROIPercent returns the ROI as a whole-number percentage.
// ROI = (resale - totalCost) / totalCost * 100
func ROIPercent(bid, repairMid, resale int) int {
	total := bid + repairMid
	if total <= 0 || resale <= 0 {
		return 0
	}
	return int(float64(resale-total) / float64(total) * 100)
}
