package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip31"
	"github.com/nbd-wtf/go-nostr/nip52"
	"github.com/nbd-wtf/go-nostr/nip53"
	"github.com/nbd-wtf/go-nostr/nip92"
	"github.com/nbd-wtf/go-nostr/nip94"
	"github.com/nbd-wtf/go-nostr/sdk"
)

type Data struct {
	templateId               TemplateID
	event                    EnhancedEvent
	nevent                   string
	neventNaked              string
	naddr                    string
	naddrNaked               string
	createdAt                string
	parentLink               template.HTML
	kindDescription          string
	kindNIP                  string
	video                    string
	videoType                string
	image                    string
	cover                    string
	content                  string
	alt                      string
	kind1063Metadata         *Kind1063Metadata
	kind30311Metadata        *Kind30311Metadata
	kind31922Or31923Metadata *Kind31922Or31923Metadata
	Kind30818Metadata        Kind30818Metadata
	Kind9802Metadata         Kind9802Metadata
	Kind1501Metadata         *Kind1501Metadata
	Kind33501Metadata        *Kind33501Metadata
	Kind30501Metadata        *Kind30501Metadata
}

type Kind1501Metadata struct {
	CourseName    string
	CourseRef     string
	Date          string
	TeeSet        string
	TotalScore    int
	Par           int
	ScoreToPar    int
	HoleScores    []HoleScore
	Players       []string
	Notes         string
	HolePars      []int // par per hole from courseSnapshot (index 0 = hole 1)
	TotalPar      int   // sum of hole pars
	HoleCount     int   // number of holes
}

type HoleScore struct {
	Hole    int
	Score   int
	Par     int
	Strokes int
}

type Kind33501Metadata struct {
	DTag           string
	Title          string
	Location       string
	Country        string
	Website        string
	Architect      string
	Established    string
	ImageURL       string
	OperatorPubkey string
	Holes          []Course33501Hole
	Tees           []Course33501Tee
	Yardages       []Course33501Yardage
	TotalPar       int
}

type Course33501Hole struct {
	Number   int
	Par      int
	Handicap int
}

type Course33501Tee struct {
	Name   string
	Rating float64
	Slope  int
}

type Course33501Yardage struct {
	Hole int
	Tee  string
	Yards int
}

type Kind30501Metadata struct {
	DTag       string
	CourseRef  string
	Date       string
	TeeSet     string
	Status     string
	Players    []string
	HoleScores []HoleScore
	TotalScore int
}

func grabData(ctx context.Context, code string, withRelays bool) (Data, error) {
	// code can be a nevent or naddr, in which case we try to fetch the associated event
	event, relays, err := getEvent(ctx, code, withRelays)
	if err != nil {
		return Data{}, fmt.Errorf("error fetching event: %w", err)
	}

	relaysForNip19 := make([]string, 0, 3)
	c := 0
	for _, relayUrl := range relays {
		if sdk.IsVirtualRelay(relayUrl) {
			continue
		}
		relaysForNip19 = append(relaysForNip19, relayUrl)
		if c == 2 {
			break
		}
	}

	ee := NewEnhancedEvent(ctx, event)
	ee.relays = relays

	data := Data{
		event: ee,
	}

	data.nevent, _ = nip19.EncodeEvent(event.ID, relaysForNip19, event.PubKey)
	data.neventNaked, _ = nip19.EncodeEvent(event.ID, nil, event.PubKey)
	data.naddr = ""
	data.naddrNaked = ""
	data.createdAt = time.Unix(int64(event.CreatedAt), 0).Format("2006-01-02 15:04:05 MST")

	if event.Kind >= 30000 && event.Kind < 40000 {
		if dTag := event.Tags.Find("d"); dTag != nil {
			data.naddr, _ = nip19.EncodeEntity(event.PubKey, event.Kind, dTag[1], relaysForNip19)
			data.naddrNaked, _ = nip19.EncodeEntity(event.PubKey, event.Kind, dTag[1], nil)
		}
	}

	data.alt = nip31.GetAlt(*event)

	switch event.Kind {
	case 1, 7, 11, 1111:
		data.templateId = Note
		data.content = event.Content
	case 30023, 30024:
		data.templateId = LongForm
		data.content = event.Content
	case 20:
		data.templateId = Note
		data.content = event.Content
	case 6:
		data.templateId = Note
		if reposted := event.Tags.Find("e"); reposted != nil {
			originalNevent, _ := nip19.EncodeEvent(reposted[1], []string{}, "")
			data.content = "Repost of nostr:" + originalNevent
		}
	case 1063:
		data.templateId = FileMetadata
		data.kind1063Metadata = &Kind1063Metadata{nip94.ParseFileMetadata(*event)}
	case 30311:
		data.templateId = LiveEvent
		data.kind30311Metadata = &Kind30311Metadata{LiveEvent: nip53.ParseLiveEvent(*event)}
		host := data.kind30311Metadata.GetHost()
		if host != nil {
			hostProfile := sys.FetchProfileMetadata(ctx, host.PubKey)
			data.kind30311Metadata.Host = &hostProfile
		}
	case 1311:
		data.templateId = LiveEventMessage
		data.content = event.Content
	case 31922, 31923:
		data.templateId = CalendarEvent
		data.kind31922Or31923Metadata = &Kind31922Or31923Metadata{CalendarEvent: nip52.ParseCalendarEvent(*event)}
		data.content = event.Content
	case 30818:
		data.templateId = WikiEvent
		data.Kind30818Metadata.Handle = event.Tags.GetD()
		data.Kind30818Metadata.Title = data.Kind30818Metadata.Handle
		if titleTag := event.Tags.Find("title"); titleTag != nil {
			data.Kind30818Metadata.Title = titleTag[1]
		}
		data.Kind30818Metadata.Summary = func() string {
			if tag := event.Tags.Find("summary"); tag != nil {
				value := tag[1]
				return value
			}
			return ""
		}()
		data.content = event.Content
	case 1501, 1502:
		data.templateId = GolfRound
		data.content = event.Content
		
		// Parse golf round metadata from NIP-101g tags
		golfData := &Kind1501Metadata{}
		
		// Parse JSON content for course_snapshot
		if event.Content != "" {
			var contentData map[string]interface{}
			if err := json.Unmarshal([]byte(event.Content), &contentData); err == nil {
				if snapshot, ok := contentData["course_snapshot"].(map[string]interface{}); ok {
					if name, ok := snapshot["course_name"].(string); ok {
						golfData.CourseName = name
					}
					if tee, ok := snapshot["tee_set"].(string); ok {
						golfData.TeeSet = tee
					}
					if holeCount, ok := snapshot["hole_count"].(float64); ok {
						golfData.HoleCount = int(holeCount)
					}
					if holes, ok := snapshot["holes"].([]interface{}); ok {
						if golfData.HoleCount == 0 {
							golfData.HoleCount = len(holes)
						}
						golfData.HolePars = make([]int, golfData.HoleCount)
						for _, h := range holes {
							if hm, ok := h.(map[string]interface{}); ok {
								num := 0
								par := 0
								if n, ok := hm["hole_number"].(float64); ok {
									num = int(n)
								}
								if p, ok := hm["par"].(float64); ok {
									par = int(p)
								}
								if num >= 1 && num <= golfData.HoleCount {
									golfData.HolePars[num-1] = par
									golfData.TotalPar += par
								}
							}
						}
					}
				}
				if notes, ok := contentData["notes"].(string); ok {
					golfData.Notes = notes
				}
			}
		}
		
		// Parse course reference
		if courseTag := event.Tags.Find("course"); courseTag != nil {
			golfData.CourseRef = courseTag[1]
			// If we don't have a course name from JSON, extract from course reference
			if golfData.CourseName == "" {
				// Format: 33501:pubkey:course_id
				parts := strings.Split(courseTag[1], ":")
				if len(parts) >= 3 {
					golfData.CourseName = parts[2] // Use course ID as fallback
				}
			}
		}
		
		// Parse date
		if dateTag := event.Tags.Find("date"); dateTag != nil {
			golfData.Date = dateTag[1]
		}
		
		// Parse tee set
		if teeTag := event.Tags.Find("tee"); teeTag != nil {
			golfData.TeeSet = teeTag[1]
		}
		
		// Parse total score
		if totalTag := event.Tags.Find("total"); totalTag != nil {
			if total, err := strconv.Atoi(totalTag[1]); err == nil {
				golfData.TotalScore = total
			}
		}
		
		// Parse individual hole scores
		var holeScores []HoleScore
		for _, tag := range event.Tags {
			if len(tag) >= 3 && tag[0] == "score" {
				if hole, err := strconv.Atoi(tag[1]); err == nil {
					if score, err := strconv.Atoi(tag[2]); err == nil {
						holeScores = append(holeScores, HoleScore{
							Hole:   hole,
							Score:  score,
							Strokes: score,
						})
					}
				}
			}
		}
		golfData.HoleScores = holeScores
		
		// Calculate par and score to par
		if golfData.TotalPar > 0 {
			golfData.Par = golfData.TotalPar
		} else {
			golfData.Par = 72 // fallback for events without courseSnapshot
		}
		golfData.ScoreToPar = golfData.TotalScore - golfData.Par
		
		// Parse players (p tags)
		var players []string
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "p" {
				players = append(players, tag[1])
			}
		}
		golfData.Players = players
		
		data.Kind1501Metadata = golfData

	case 33501:
		data.templateId = CourseData
		data.content = event.Content

		courseData := &Kind33501Metadata{}

		// d tag
		if dTag := event.Tags.Find("d"); dTag != nil {
			courseData.DTag = dTag[1]
		}

		// title
		if titleTag := event.Tags.Find("title"); titleTag != nil {
			courseData.Title = titleTag[1]
		}

		// location
		if locTag := event.Tags.Find("location"); locTag != nil {
			courseData.Location = locTag[1]
		}

		// country
		if countryTag := event.Tags.Find("country"); countryTag != nil {
			courseData.Country = countryTag[1]
		}

		// website
		if webTag := event.Tags.Find("website"); webTag != nil {
			courseData.Website = webTag[1]
		}

		// architect
		if archTag := event.Tags.Find("architect"); archTag != nil {
			courseData.Architect = archTag[1]
		}

		// established
		if estTag := event.Tags.Find("established"); estTag != nil {
			courseData.Established = estTag[1]
		}

		// image (hero image)
		if imgTag := event.Tags.Find("image"); imgTag != nil {
			courseData.ImageURL = imgTag[1]
		}

		// operator pubkey from p tag with "operator" role
		for _, tag := range event.Tags {
			if len(tag) >= 4 && tag[0] == "p" && tag[3] == "operator" {
				courseData.OperatorPubkey = tag[1]
				break
			}
		}

		// holes
		for _, tag := range event.Tags {
			if len(tag) >= 4 && tag[0] == "hole" {
				num, _ := strconv.Atoi(tag[1])
				par, _ := strconv.Atoi(tag[2])
				hcp, _ := strconv.Atoi(tag[3])
				courseData.Holes = append(courseData.Holes, Course33501Hole{
					Number:   num,
					Par:      par,
					Handicap: hcp,
				})
			}
		}

		// tees
		for _, tag := range event.Tags {
			if len(tag) >= 4 && tag[0] == "tee" {
				rating, _ := strconv.ParseFloat(tag[2], 64)
				slope, _ := strconv.Atoi(tag[3])
				courseData.Tees = append(courseData.Tees, Course33501Tee{
					Name:   tag[1],
					Rating: rating,
					Slope:  slope,
				})
			}
		}

		// yardages
		for _, tag := range event.Tags {
			if len(tag) >= 4 && tag[0] == "yardage" {
				hole, _ := strconv.Atoi(tag[1])
				yards, _ := strconv.Atoi(tag[3])
				courseData.Yardages = append(courseData.Yardages, Course33501Yardage{
					Hole:  hole,
					Tee:   tag[2],
					Yards: yards,
				})
			}
		}

		// calculate total par
		totalPar := 0
		for _, h := range courseData.Holes {
			totalPar += h.Par
		}
		courseData.TotalPar = totalPar

		data.Kind33501Metadata = courseData

	case 30501:
		data.templateId = LiveScorecard
		data.content = event.Content

		liveData := &Kind30501Metadata{}

		// d tag
		if dTag := event.Tags.Find("d"); dTag != nil {
			liveData.DTag = dTag[1]
		}

		// course reference
		if courseTag := event.Tags.Find("course"); courseTag != nil {
			liveData.CourseRef = courseTag[1]
		}

		// date
		if dateTag := event.Tags.Find("date"); dateTag != nil {
			liveData.Date = dateTag[1]
		}

		// tee set
		if teeTag := event.Tags.Find("tee"); teeTag != nil {
			liveData.TeeSet = teeTag[1]
		}

		// status
		if statusTag := event.Tags.Find("status"); statusTag != nil {
			liveData.Status = statusTag[1]
		}

		// players (p tags)
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "p" {
				liveData.Players = append(liveData.Players, tag[1])
			}
		}

		// hole scores
		for _, tag := range event.Tags {
			if len(tag) >= 3 && tag[0] == "score" {
				hole, _ := strconv.Atoi(tag[1])
				score, _ := strconv.Atoi(tag[2])
				liveData.HoleScores = append(liveData.HoleScores, HoleScore{
					Hole:    hole,
					Score:   score,
					Strokes: score,
				})
			}
		}

		// calculate total score
		total := 0
		for _, hs := range liveData.HoleScores {
			total += hs.Score
		}
		liveData.TotalScore = total

		data.Kind30501Metadata = liveData

	case 9802:
		data.templateId = Highlight
		data.content = event.Content
		if sourceEvent := event.Tags.Find("e"); sourceEvent != nil {
			data.Kind9802Metadata.SourceEvent = sourceEvent[1]
			data.Kind9802Metadata.SourceName = "#" + shortenString(sourceEvent[1], 8, 4)
		} else if sourceEvent := event.Tags.Find("a"); sourceEvent != nil {
			spl := strings.Split(sourceEvent[1], ":")
			kind, _ := strconv.Atoi(spl[0])
			var relayHints []string
			if len(sourceEvent) > 2 {
				relayHints = []string{sourceEvent[2]}
			}
			naddr, _ := nip19.EncodeEntity(spl[1], kind, spl[2], relayHints)
			data.Kind9802Metadata.SourceEvent = naddr
		} else if sourceUrl := event.Tags.Find("r"); sourceUrl != nil {
			data.Kind9802Metadata.SourceURL = sourceUrl[1]
			data.Kind9802Metadata.SourceName = sourceUrl[1]
		}

		if data.Kind9802Metadata.SourceEvent != "" {
			// Retrieve the title
			sourceEvent, _, _ := getEvent(ctx, data.Kind9802Metadata.SourceEvent, withRelays)
			if title := sourceEvent.Tags.Find("title"); title != nil {
				data.Kind9802Metadata.SourceName = title[1]
			} else {
				data.Kind9802Metadata.SourceName = "Note dated " + sourceEvent.CreatedAt.Time().Format("January 1, 2006 15:04")
			}
			// Retrieve the author using the event, ignore the `p` tag in the highlight event
			ctx, cancel := context.WithTimeout(ctx, time.Second*3)
			defer cancel()
			data.Kind9802Metadata.Author = sys.FetchProfileMetadata(ctx, sourceEvent.PubKey)
		}
		if author := event.Tags.Find("p"); author != nil {
			ctx, cancel := context.WithTimeout(ctx, time.Second*3)
			defer cancel()
			data.Kind9802Metadata.Author = sys.FetchProfileMetadata(ctx, author[1])
		}
		if context := event.Tags.Find("context"); context != nil {
			data.Kind9802Metadata.Context = context[1]

			escapedContext := html.EscapeString(context[1])
			escapedCitation := html.EscapeString(data.content)

			// Some clients mistakenly put the highlight in the context
			if escapedContext != escapedCitation {
				// Replace the citation with the marked version
				data.Kind9802Metadata.MarkedContext = strings.Replace(
					escapedContext,
					escapedCitation,
					fmt.Sprintf("<span class=\"bg-amber-100 dark:bg-amber-700\">%s</span>", escapedCitation),
					-1, // Replace all occurrences
				)
			}
		}
		if comment := event.Tags.Find("comment"); comment != nil {
			data.Kind9802Metadata.Comment = basicFormatting(comment[1], false, false, false)
		}

	default:
		data.templateId = Other
	}

	data.kindDescription = kindNames[event.Kind]
	if data.kindDescription == "" {
		data.kindDescription = fmt.Sprintf("Kind %d", event.Kind)
	}
	data.kindNIP = kindNIPs[event.Kind]

	image := event.Tags.Find("image")
	if event.Kind == 30023 && image != nil {
		data.cover = image[1]
	} else if event.Kind == 1063 {
		if data.kind1063Metadata.IsImage() {
			data.image = data.kind1063Metadata.URL
		} else if data.kind1063Metadata.IsVideo() {
			data.video = data.kind1063Metadata.URL
			data.videoType = strings.Split(data.kind1063Metadata.M, "/")[1]
		}
	} else if event.Kind == 20 {
		imeta := nip92.ParseTags(event.Tags)
		if len(imeta) > 0 {
			data.image = imeta[0].URL

			content := strings.Builder{}
			content.Grow(110*len(imeta) + len(data.content))
			for _, entry := range imeta {
				content.WriteString(entry.URL)
				content.WriteString(" ")
			}
			content.WriteString(data.content)
			data.content = content.String()
		}
		if tag := data.event.Tags.Find("title"); tag != nil {
			data.event.subject = tag[1]
		}
	} else {
		urls := urlMatcher.FindAllString(event.Content, -1)
		for _, url := range urls {
			switch {
			case imageExtensionMatcher.MatchString(url):
				if data.image == "" {
					data.image = url
				}
			case videoExtensionMatcher.MatchString(url):
				if data.video == "" {
					data.video = url
					if strings.HasSuffix(data.video, "mp4") {
						data.videoType = "mp4"
					} else if strings.HasSuffix(data.video, "mov") {
						data.videoType = "mov"
					} else {
						data.videoType = "webm"
					}
				}
			}
		}
	}

	return data, nil
}
