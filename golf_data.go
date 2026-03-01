package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const gambitRelay = "wss://relay.gambit.golf"

// RoundPageData is the assembled data for rendering a multi-player round page.
// Built from: 1501 (initiation) + 1502s (final records) + 31501s (live scorecards) + kind 0 (profiles).
type RoundPageData struct {
	// From 1501
	CourseName string
	TeeSet     string
	Date       string
	HoleCount  int
	HolePars   []int // par per hole (index 0 = hole 1)
	TotalPar   int
	Notes      string

	// Players from p-tags, resolved via kind 0
	Players []PlayerData

	// From 1502s + 31501s
	PlayerScores []PlayerScoreData

	// Round state (derived)
	State           string // "live" | "final" | "waiting"
	PlayersTotal    int
	PlayersFinished int
}

type PlayerData struct {
	PubkeyHex   string
	DisplayName string
	Picture     string
	Role        string // "player" | "bot"
	Npub        string
}

type PlayerScoreData struct {
	Player     PlayerData
	HoleScores []int  // score per hole (index 0 = hole 1, 0 = not played)
	Total      int
	ScoreToPar int
	IsFinal    bool   // true if from 1502, false if from 31501
	EventId    string // event ID for linking
}

// buildRoundPageData constructs the full round page data from a 1501 event.
// Fetches 1502s, 31501s, and profiles from relay.gambit.golf.
func buildRoundPageData(ctx context.Context, event *nostr.Event, metadata *Kind1501Metadata) RoundPageData {
	rpd := RoundPageData{
		CourseName: metadata.CourseName,
		TeeSet:     metadata.TeeSet,
		Date:       metadata.Date,
		HoleCount:  metadata.HoleCount,
		HolePars:   metadata.HolePars,
		TotalPar:   metadata.TotalPar,
		Notes:      metadata.Notes,
	}

	// Parse players from p-tags with roles
	var playerPubkeys []string
	playerRoles := make(map[string]string) // pubkey -> role
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			pk := tag[1]
			role := "player"
			if len(tag) >= 4 {
				role = tag[3]
			}
			playerPubkeys = append(playerPubkeys, pk)
			playerRoles[pk] = role
		}
	}
	rpd.PlayersTotal = 0
	for _, role := range playerRoles {
		if role == "player" {
			rpd.PlayersTotal++
		}
	}

	// Fetch 1502s and 31501s from relay
	records, livecards := fetchScores(ctx, event.ID)

	// Resolve player profiles
	profiles := fetchPlayerProfiles(ctx, playerPubkeys)

	// Build player list
	for _, pk := range playerPubkeys {
		pd := profiles[pk]
		pd.Role = playerRoles[pk]
		rpd.Players = append(rpd.Players, pd)
	}

	// Build player scores: prefer 1502 (final) over 31501 (live)
	finalByAuthor := make(map[string]*nostr.Event)
	for _, rec := range records {
		finalByAuthor[rec.PubKey] = rec
	}
	liveByAuthor := make(map[string]*nostr.Event)
	for _, lc := range livecards {
		liveByAuthor[lc.PubKey] = lc
	}

	for _, pk := range playerPubkeys {
		role := playerRoles[pk]
		if role == "bot" {
			continue // hide bot scores per resolved decision
		}

		pd := profiles[pk]
		pd.Role = role

		if finalEvt, ok := finalByAuthor[pk]; ok {
			// Final record
			psd := parseScoreEvent(finalEvt, pd, rpd.HolePars, rpd.TotalPar)
			psd.IsFinal = true
			rpd.PlayerScores = append(rpd.PlayerScores, psd)
			rpd.PlayersFinished++
		} else if liveEvt, ok := liveByAuthor[pk]; ok {
			// Live scorecard
			psd := parseScoreEvent(liveEvt, pd, rpd.HolePars, rpd.TotalPar)
			psd.IsFinal = false
			rpd.PlayerScores = append(rpd.PlayerScores, psd)
		}
	}

	// Determine round state
	if rpd.PlayersFinished == rpd.PlayersTotal && rpd.PlayersTotal > 0 {
		rpd.State = "final"
	} else if rpd.PlayersFinished > 0 || len(livecards) > 0 {
		rpd.State = "live"
	} else {
		rpd.State = "waiting"
	}

	return rpd
}

// parseScoreEvent extracts hole scores from a 1502 or 31501 event.
func parseScoreEvent(evt *nostr.Event, player PlayerData, holePars []int, totalPar int) PlayerScoreData {
	psd := PlayerScoreData{
		Player:  player,
		EventId: evt.ID,
	}

	// Initialize hole scores (0 = not played)
	holeCount := len(holePars)
	if holeCount == 0 {
		holeCount = 18
	}
	psd.HoleScores = make([]int, holeCount)

	for _, tag := range evt.Tags {
		if len(tag) >= 3 && tag[0] == "score" {
			hole, err := strconv.Atoi(tag[1])
			if err != nil || hole < 1 || hole > holeCount {
				continue
			}
			score, err := strconv.Atoi(tag[2])
			if err != nil {
				continue
			}
			psd.HoleScores[hole-1] = score
			psd.Total += score
		}
	}

	// Check for explicit total tag (1502s have this)
	if totalTag := evt.Tags.GetFirst([]string{"total", ""}); totalTag != nil && len(*totalTag) >= 2 {
		if t, err := strconv.Atoi((*totalTag)[1]); err == nil {
			psd.Total = t
		}
	}

	if totalPar > 0 && psd.Total > 0 {
		psd.ScoreToPar = psd.Total - totalPar
	}

	return psd
}

// fetchScores queries relay.gambit.golf for 1502s and 31501s referencing a 1501 event.
func fetchScores(ctx context.Context, initiationEventID string) (records []*nostr.Event, livecards []*nostr.Event) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	relay, err := sys.Pool.EnsureRelay(gambitRelay)
	if err != nil {
		log.Warn().Err(err).Msg("failed to connect to gambit relay for scores")
		return
	}

	// Fetch 1502s (final records) — #e = 1501 ID
	filter1502 := nostr.Filter{
		Kinds: []int{1502},
		Tags:  nostr.TagMap{"e": {initiationEventID}},
	}

	ch1502, err := relay.QueryEvents(ctx, filter1502)
	if err != nil {
		log.Warn().Err(err).Msg("failed to query 1502s")
	} else {
		for evt := range ch1502 {
			records = append(records, evt)
		}
	}

	// Fetch 31501s (live scorecards) — #e = 1501 ID
	filter31501 := nostr.Filter{
		Kinds: []int{31501},
		Tags:  nostr.TagMap{"e": {initiationEventID}},
	}

	ch31501, err := relay.QueryEvents(ctx, filter31501)
	if err != nil {
		log.Warn().Err(err).Msg("failed to query 31501s")
	} else {
		for evt := range ch31501 {
			livecards = append(livecards, evt)
		}
	}

	return
}

// fetchPlayerProfiles resolves kind 0 profiles for a list of pubkeys.
func fetchPlayerProfiles(ctx context.Context, pubkeys []string) map[string]PlayerData {
	profiles := make(map[string]PlayerData, len(pubkeys))

	for _, pk := range pubkeys {
		npub, _ := nip19.EncodePublicKey(pk)
		pd := PlayerData{
			PubkeyHex: pk,
			Npub:      npub,
		}

		// Use the existing SDK profile fetcher
		meta := sys.FetchProfileMetadata(ctx, pk)
		if meta.Name != "" {
			pd.DisplayName = meta.Name
		} else if meta.DisplayName != "" {
			pd.DisplayName = meta.DisplayName
		} else {
			// Fallback to truncated npub
			pd.DisplayName = shortenString(npub, 8, 4)
		}
		pd.Picture = meta.Picture

		profiles[pk] = pd
	}

	return profiles
}

// Template helper functions for the multi-player scorecard

func scoreDisplay(scores []int, idx int) string {
	if idx < 0 || idx >= len(scores) || scores[idx] == 0 {
		return "-"
	}
	return strconv.Itoa(scores[idx])
}

func scoreCellClass(scores []int, pars []int, idx int) string {
	base := "border border-gray-800 px-1.5 py-2 text-sm font-bold font-mono"
	if idx < 0 || idx >= len(scores) || scores[idx] == 0 {
		return base + " text-gray-400"
	}
	if idx >= len(pars) || pars[idx] == 0 {
		return base + " text-gray-900"
	}
	diff := scores[idx] - pars[idx]
	if diff < 0 {
		return base + " text-green-700 bg-green-50"
	} else if diff > 0 {
		return base + " text-red-700 bg-red-50"
	}
	return base + " text-gray-900"
}

func totalScoreClass(scoreToPar int) string {
	if scoreToPar < 0 {
		return "text-green-700"
	} else if scoreToPar > 0 {
		return "text-red-700"
	}
	return "text-blue-700"
}

func formatScoreToPar(scoreToPar int) string {
	if scoreToPar > 0 {
		return fmt.Sprintf("+%d", scoreToPar)
	} else if scoreToPar < 0 {
		return strconv.Itoa(scoreToPar)
	}
	return "E"
}

func nineTotal(scores []int, start, end int) string {
	total := 0
	count := 0
	for i := start; i < end && i < len(scores); i++ {
		if scores[i] > 0 {
			total += scores[i]
			count++
		}
	}
	if count == 0 {
		return "-"
	}
	return strconv.Itoa(total)
}

func sumSlice(vals []int, start, end int) int {
	total := 0
	for i := start; i < end && i < len(vals); i++ {
		total += vals[i]
	}
	return total
}

func formatDate(dateStr string) string {
	if len(dateStr) >= 10 {
		return dateStr[:10]
	}
	return dateStr
}

// Legacy helpers for golf_scorecard_page.templ (single-player 1502 view)

func getScoreForHole(holeScores []HoleScore, hole int) string {
	for _, score := range holeScores {
		if score.Hole == hole {
			return strconv.Itoa(score.Score)
		}
	}
	return "-"
}

func getFrontNineTotal(holeScores []HoleScore) string {
	total := 0
	count := 0
	for _, score := range holeScores {
		if score.Hole >= 1 && score.Hole <= 9 {
			total += score.Score
			count++
		}
	}
	if count == 0 {
		return "-"
	}
	return strconv.Itoa(total)
}

func getBackNineTotal(holeScores []HoleScore) string {
	total := 0
	count := 0
	for _, score := range holeScores {
		if score.Hole >= 10 && score.Hole <= 18 {
			total += score.Score
			count++
		}
	}
	if count == 0 {
		return "-"
	}
	return strconv.Itoa(total)
}
