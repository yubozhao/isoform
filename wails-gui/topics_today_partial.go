package main

const topicTodayHistoryLimit = 64

// idx_activities_end also orders equal timestamps by rowid. Following that
// order avoids sorting a whole tied batch before verifying its first rows.
const topicTodayRecentQuery = `SELECT ` + activityColumns + ` FROM activities WHERE end_ms>=? AND start_ms<=? ORDER BY end_ms DESC,rowid DESC LIMIT 4`
