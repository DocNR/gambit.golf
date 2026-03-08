package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// TournamentMetadata is parsed from kind 31923 event tags in data.go.
type TournamentMetadata struct {
	Title            string
	Location         string
	StartUnix        int64
	TournamentStatus string // registration_open / registration_closed / in_progress / complete
	CourseCoord      string // "33501:<pubkey>:<d>"
	TeeSet           string
	RosterPubkeys    []string
	Image            string
}

// TournamentPageData is the assembled leaderboard data for rendering.
type TournamentPageData struct {
	Title            string
	Location         string
	Date             string
	TournamentStatus string
	Image            string
	TeeSet           string
	CoursePar        int
	Players          []LeaderboardEntry
	Naddr            string
}

// LeaderboardEntry represents one player row on the leaderboard.
type LeaderboardEntry struct {
	Rank       string // "1", "T2", "T2", "4"...
	Player     PlayerData
	ScoreToPar int
	Total      int
	Thru       string // "F", "9", "18", "-"
	IsFinished bool
	IsDNS      bool // on roster but no 1501
	IsPlaying  bool // in_progress 31501
}

// buildTournamentPageData constructs the full leaderboard from a kind 31923 tournament event.
func buildTournamentPageData(ctx context.Context, tournamentEvent *nostr.Event, meta *TournamentMetadata, naddr string) TournamentPageData {
	tpd := TournamentPageData{
		Title:            meta.Title,
		Location:         meta.Location,
		TournamentStatus: meta.TournamentStatus,
		Image:            meta.Image,
		TeeSet:           meta.TeeSet,
		Naddr:            naddr,
		CoursePar:        72, // default fallback
	}

	// Format date from start unix timestamp
	if meta.StartUnix > 0 {
		tpd.Date = time.Unix(meta.StartUnix, 0).Format("2006-01-02")
	}

	// Build the "a" tag coordinate for this tournament
	dTag := tournamentEvent.Tags.GetD()
	aCoord := fmt.Sprintf("31923:%s:%s", tournamentEvent.PubKey, dTag)

	// Try to fetch course par from 33501
	if meta.CourseCoord != "" {
		coursePar := fetchCoursePar(ctx, meta.CourseCoord)
		if coursePar > 0 {
			tpd.CoursePar = coursePar
		}
	}

	// Query 1: kind 1501s linked to this tournament via #a tag
	initiations := fetchTournament1501s(ctx, aCoord)

	// Dedup 1501s by author (prefer the one with scores, else latest)
	initByAuthor := make(map[string]*nostr.Event)
	for _, evt := range initiations {
		existing, ok := initByAuthor[evt.PubKey]
		if !ok || evt.CreatedAt > existing.CreatedAt {
			initByAuthor[evt.PubKey] = evt
		}
	}

	// Collect 1501 event IDs for querying 1502s and 31501s
	var initIDs []string
	initIDToAuthor := make(map[string]string) // 1501 ID → author pubkey
	for _, evt := range initByAuthor {
		initIDs = append(initIDs, evt.ID)
		initIDToAuthor[evt.ID] = evt.PubKey
	}

	// Query 2: 1502s and 31501s referencing the 1501s
	records, livecards := fetchTournamentScores(ctx, initIDs)

	// Map 1502s by the author of the 1501 they reference
	finalByPlayer := make(map[string]*nostr.Event)
	for _, rec := range records {
		// Find which 1501 this 1502 references
		for _, tag := range rec.Tags {
			if len(tag) >= 2 && tag[0] == "e" {
				if authorPK, ok := initIDToAuthor[tag[1]]; ok {
					finalByPlayer[authorPK] = rec
					break
				}
			}
		}
	}

	// Map 31501s by the author of the 1501 they reference
	liveByPlayer := make(map[string]*nostr.Event)
	for _, lc := range livecards {
		for _, tag := range lc.Tags {
			if len(tag) >= 2 && tag[0] == "e" {
				if authorPK, ok := initIDToAuthor[tag[1]]; ok {
					liveByPlayer[authorPK] = lc
					break
				}
			}
		}
	}

	// Build roster set (from tournament p-tags + anyone who submitted a 1501)
	rosterSet := make(map[string]bool)
	for _, pk := range meta.RosterPubkeys {
		rosterSet[pk] = true
	}
	for pk := range initByAuthor {
		rosterSet[pk] = true
	}

	// Collect all pubkeys for profile fetching
	var allPubkeys []string
	for pk := range rosterSet {
		allPubkeys = append(allPubkeys, pk)
	}

	// Query 3: profiles
	profiles := fetchPlayerProfiles(ctx, allPubkeys)

	// Build leaderboard entries
	var entries []LeaderboardEntry
	for pk := range rosterSet {
		entry := LeaderboardEntry{
			Player: profiles[pk],
		}

		if finalEvt, ok := finalByPlayer[pk]; ok {
			// Has a 1502 — finished
			entry.IsFinished = true
			entry.Thru = "F"
			entry.Total = parseTotalFromEvent(finalEvt)
			if entry.Total > 0 && tpd.CoursePar > 0 {
				entry.ScoreToPar = entry.Total - tpd.CoursePar
			}
		} else if liveEvt, ok := liveByPlayer[pk]; ok {
			// Has a 31501 — in progress
			entry.IsPlaying = true
			total, holesPlayed := parseLiveScorecardScores(liveEvt)
			entry.Total = total
			entry.Thru = strconv.Itoa(holesPlayed)
			if entry.Total > 0 && tpd.CoursePar > 0 {
				entry.ScoreToPar = entry.Total - tpd.CoursePar
			}
		} else if _, ok := initByAuthor[pk]; ok {
			// Has a 1501 but no scores yet — treat as playing with no holes
			entry.IsPlaying = true
			entry.Thru = "-"
		} else {
			// On roster but no 1501 — DNS
			entry.IsDNS = true
			entry.Thru = "-"
		}

		entries = append(entries, entry)
	}

	// Sort: finished (asc scoreToPar) → in progress (asc scoreToPar) → DNS
	sort.SliceStable(entries, func(i, j int) bool {
		ci := sortCategory(entries[i])
		cj := sortCategory(entries[j])
		if ci != cj {
			return ci < cj
		}
		if entries[i].IsDNS && entries[j].IsDNS {
			return entries[i].Player.DisplayName < entries[j].Player.DisplayName
		}
		return entries[i].ScoreToPar < entries[j].ScoreToPar
	})

	// Assign ranks with ties
	assignRanks(entries)

	tpd.Players = entries
	return tpd
}

// sortCategory returns a sort priority: finished=0, playing=1, DNS=2
func sortCategory(e LeaderboardEntry) int {
	if e.IsFinished {
		return 0
	}
	if e.IsPlaying {
		return 1
	}
	return 2 // DNS
}

// assignRanks assigns standard golf ranking with ties ("T2" for tied 2nd).
func assignRanks(entries []LeaderboardEntry) {
	if len(entries) == 0 {
		return
	}

	rank := 1
	for i := range entries {
		if entries[i].IsDNS {
			entries[i].Rank = "-"
			continue
		}

		if i > 0 && !entries[i-1].IsDNS &&
			entries[i].ScoreToPar == entries[i-1].ScoreToPar &&
			entries[i].IsFinished == entries[i-1].IsFinished {
			// Same rank as previous
			entries[i].Rank = entries[i-1].Rank
		} else {
			entries[i].Rank = strconv.Itoa(rank)
		}
		rank = i + 2 // next rank = position + 1 (1-indexed)
	}

	// Now mark ties with "T" prefix
	rankCounts := make(map[string]int)
	for _, e := range entries {
		if e.Rank != "-" {
			rankCounts[e.Rank]++
		}
	}
	for i := range entries {
		if entries[i].Rank != "-" && rankCounts[entries[i].Rank] > 1 {
			if !strings.HasPrefix(entries[i].Rank, "T") {
				entries[i].Rank = "T" + entries[i].Rank
			}
		}
	}
}

// parseTotalFromEvent reads the total tag from a 1502 event.
func parseTotalFromEvent(evt *nostr.Event) int {
	if totalTag := evt.Tags.GetFirst([]string{"total", ""}); totalTag != nil && len(*totalTag) >= 2 {
		if t, err := strconv.Atoi((*totalTag)[1]); err == nil {
			return t
		}
	}
	// Fallback: sum score tags
	total := 0
	for _, tag := range evt.Tags {
		if len(tag) >= 3 && tag[0] == "score" {
			if s, err := strconv.Atoi(tag[2]); err == nil {
				total += s
			}
		}
	}
	return total
}

// parseLiveScorecardScores reads scores from a 31501 live scorecard event.
// Returns total strokes and number of holes played.
func parseLiveScorecardScores(evt *nostr.Event) (total int, holesPlayed int) {
	// Try JSON content first (newer format: {"scores": [{holeNumber, strokes}]})
	if evt.Content != "" {
		var contentData map[string]interface{}
		if err := json.Unmarshal([]byte(evt.Content), &contentData); err == nil {
			if scores, ok := contentData["scores"].([]interface{}); ok {
				for _, s := range scores {
					if sm, ok := s.(map[string]interface{}); ok {
						if strokes, ok := sm["strokes"].(float64); ok && strokes > 0 {
							total += int(strokes)
							holesPlayed++
						}
					}
				}
				if holesPlayed > 0 {
					return
				}
			}
		}
	}

	// Fallback: score tags
	for _, tag := range evt.Tags {
		if len(tag) >= 3 && tag[0] == "score" {
			if s, err := strconv.Atoi(tag[2]); err == nil && s > 0 {
				total += s
				holesPlayed++
			}
		}
	}
	return
}

// fetchTournament1501s queries relay.gambit.golf for kind 1501 events
// referencing the given tournament "a" coordinate.
func fetchTournament1501s(ctx context.Context, aCoord string) []*nostr.Event {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	relay, err := sys.Pool.EnsureRelay(gambitRelay)
	if err != nil {
		log.Warn().Err(err).Msg("failed to connect to gambit relay for tournament 1501s")
		return nil
	}

	filter := nostr.Filter{
		Kinds: []int{1501},
		Tags:  nostr.TagMap{"a": {aCoord}},
	}

	ch, err := relay.QueryEvents(ctx, filter)
	if err != nil {
		log.Warn().Err(err).Msg("failed to query tournament 1501s")
		return nil
	}

	var events []*nostr.Event
	for evt := range ch {
		events = append(events, evt)
	}
	return events
}

// fetchTournamentScores queries relay.gambit.golf for 1502s and 31501s
// referencing any of the given 1501 event IDs.
func fetchTournamentScores(ctx context.Context, initIDs []string) (records []*nostr.Event, livecards []*nostr.Event) {
	if len(initIDs) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	relay, err := sys.Pool.EnsureRelay(gambitRelay)
	if err != nil {
		log.Warn().Err(err).Msg("failed to connect to gambit relay for tournament scores")
		return
	}

	// Fetch 1502s (final records)
	filter1502 := nostr.Filter{
		Kinds: []int{1502},
		Tags:  nostr.TagMap{"e": initIDs},
	}

	ch1502, err := relay.QueryEvents(ctx, filter1502)
	if err != nil {
		log.Warn().Err(err).Msg("failed to query tournament 1502s")
	} else {
		for evt := range ch1502 {
			records = append(records, evt)
		}
	}

	// Fetch 31501s (live scorecards)
	filter31501 := nostr.Filter{
		Kinds: []int{31501},
		Tags:  nostr.TagMap{"e": initIDs},
	}

	ch31501, err := relay.QueryEvents(ctx, filter31501)
	if err != nil {
		log.Warn().Err(err).Msg("failed to query tournament 31501s")
	} else {
		for evt := range ch31501 {
			livecards = append(livecards, evt)
		}
	}

	return
}

// fetchCoursePar fetches the kind 33501 event from the course coordinate
// and sums hole pars. Returns 0 if unavailable.
func fetchCoursePar(ctx context.Context, courseCoord string) int {
	// Parse "33501:<pubkey>:<d>"
	parts := strings.Split(courseCoord, ":")
	if len(parts) < 3 || parts[0] != "33501" {
		return 0
	}
	authorPK := parts[1]
	dTag := parts[2]

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	relay, err := sys.Pool.EnsureRelay(gambitRelay)
	if err != nil {
		return 0
	}

	filter := nostr.Filter{
		Kinds:   []int{33501},
		Authors: []string{authorPK},
		Tags:    nostr.TagMap{"d": {dTag}},
		Limit:   1,
	}

	ch, err := relay.QueryEvents(ctx, filter)
	if err != nil {
		return 0
	}

	var courseEvt *nostr.Event
	for evt := range ch {
		courseEvt = evt
		break
	}
	if courseEvt == nil {
		return 0
	}

	totalPar := 0
	for _, tag := range courseEvt.Tags {
		if len(tag) >= 3 && tag[0] == "hole" {
			if par, err := strconv.Atoi(tag[2]); err == nil {
				totalPar += par
			}
		}
	}

	return totalPar
}

// Tournament template helper functions

func tournamentStatusBadgeClass(status string) string {
	switch status {
	case "registration_open":
		return "bg-blue-600 text-white"
	case "registration_closed":
		return "bg-yellow-500 text-white"
	case "in_progress":
		return "bg-green-600 text-white"
	case "complete":
		return "bg-gray-600 text-white"
	default:
		return "bg-gray-600 text-white"
	}
}

func tournamentStatusLabel(status string) string {
	switch status {
	case "registration_open":
		return "Registration Open"
	case "registration_closed":
		return "Registration Closed"
	case "in_progress":
		return "Live"
	case "complete":
		return "Final"
	default:
		return status
	}
}

func leaderboardScoreDisplay(e LeaderboardEntry) string {
	if e.IsDNS {
		return "DNS"
	}
	if e.Total == 0 && !e.IsFinished {
		return "-"
	}
	return formatScoreToPar(e.ScoreToPar)
}

func leaderboardScoreClass(e LeaderboardEntry) string {
	if e.IsDNS {
		return "text-gray-400"
	}
	if e.Total == 0 && !e.IsFinished {
		return "text-gray-400"
	}
	if e.ScoreToPar < 0 {
		return "text-red-600 font-bold"
	} else if e.ScoreToPar > 0 {
		return "text-gray-900"
	}
	return "text-blue-600"
}

func tournamentOGDescription(tpd TournamentPageData) string {
	if len(tpd.Players) > 0 {
		for _, p := range tpd.Players {
			if !p.IsDNS {
				return fmt.Sprintf("Leader: %s (%s)", p.Player.DisplayName, leaderboardScoreDisplay(p))
			}
		}
	}
	if tpd.TournamentStatus == "registration_open" {
		return "Registration Open"
	}
	return tpd.Title
}
