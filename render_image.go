package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fogleman/gg"
	"github.com/go-text/typesetting/shaping"
	"github.com/golang/freetype/truetype"
	"github.com/nbd-wtf/go-nostr/sdk"
	"github.com/nfnt/resize"
	xfont "golang.org/x/image/font"
)

const (
	BLOCK = "|"
)

var (
	BACKGROUND     = color.RGBA{23, 23, 23, 255}
	BAR_BACKGROUND = color.RGBA{10, 10, 10, 255}
	FOREGROUND     = color.RGBA{255, 230, 238, 255}
)

//go:embed fonts/*
var fonts embed.FS

var multiNewlineRe = regexp.MustCompile(`\n\n+`)

func renderImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	code := r.URL.Path[1+len("njump/image/"):]
	if code == "" {
		fmt.Fprintf(w, "call /njump/image/<nip19 code>")
		return
	}

	// trim fake extensions
	extensions := []string{".png", ".jpg", ".jpeg"}
	for _, ext := range extensions {
		code = strings.TrimSuffix(code, ext)
	}

	data, err := grabData(ctx, code, false)
	if err != nil {
		http.Error(w, "error fetching event: "+err.Error(), http.StatusNotFound)
		log.Warn().Err(err).Str("code", code).Msg("event not found on render_image")
		return
	}

	// banned or unallowed conditions
	if banned, _ := internal.isBannedEvent(data.event.ID); banned {
		w.WriteHeader(http.StatusNotFound)
		http.Error(w, "event banned", http.StatusNotFound)
		return
	}
	if banned, _ := internal.isBannedPubkey(data.event.PubKey); banned {
		w.WriteHeader(http.StatusNotFound)
		http.Error(w, "pubkey banned", http.StatusNotFound)
		return
	}
	hasURL := urlRegex.MatchString(data.event.Content)
	if isMaliciousBridged(data.event.author) ||
		(hasURL && hasProhibitedWordOrTag(data.event.Event)) ||
		(hasURL && hasExplicitMedia(ctx, data.event.Event)) {
		http.Error(w, "event is not allowed", http.StatusNotFound)
		return
	}

	// Check if this is a golf event (Kind 1501) and generate custom golf scorecard image
	if data.event.Kind == 1501 {
		img, err := drawGolfScorecardImage(ctx, *data.Kind1501Metadata, data.event.author, data.event.CreatedAt.Time())
		if err != nil {
			log.Warn().Err(err).Msg("failed to draw golf scorecard image")
			http.Error(w, "error writing golf scorecard image!", 500)
			return
		}
		
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=604800")

		if err := png.Encode(w, img); err != nil {
			log.Printf("error encoding golf scorecard image: %s", err)
			return
		}
		return
	}

	content := data.event.Content
	content = strings.Replace(content, "\r\n", "\n", -1)
	content = multiNewlineRe.ReplaceAllString(content, "\n\n")
	content = strings.Replace(content, "\t", "  ", -1)
	content = strings.Replace(content, "\r", "", -1)
	content = shortenURLs(content, true)
	if len(content) > 650 {
		content = content[0:650]
	}

	// this turns the raw event.Content into a series of lines ready to drawn
	paragraphs := replaceUserReferencesWithNames(ctx,
		quotesAsBlockPrefixedText(ctx,
			strings.Split(content, "\n"),
		),
		string(INVISIBLE_SPACE),
	)

	img, err := drawImage(ctx, paragraphs, getPreviewStyle(r), data.event.author, data.event.CreatedAt.Time())
	if err != nil {
		log.Warn().Err(err).Msg("failed to draw paragraphs as image")
		http.Error(w, "error writing image!", 500)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=604800")

	if err := png.Encode(w, img); err != nil {
		log.Printf("error encoding image: %s", err)
		return
	}
}

func drawImage(
	ctx context.Context,
	paragraphs []string,
	style Style,
	metadata sdk.ProfileMetadata,
	date time.Time,
) (image image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic while drawing image")
			log.Warn().Interface("r", r).Msg("panic while drawing image")
		}
	}()

	fontSize := 25
	width := 700
	height := 525
	paddingLeft := 25
	gradientRectHeight := 140
	barHeight := 70
	barScale := 1.0
	switch style {
	case StyleTelegram:
		paddingLeft += 10
		width -= 10
	case StyleTwitter:
		height = width * 268 / 512
	case StyleFacebook:
		height = width * 355 / 680
		paddingLeft = 180
		barScale = 0.55
		barHeight = int(float64(barHeight) * barScale)
		fontSize = 18
	}

	img := gg.NewContext(width, height)
	img.SetColor(BACKGROUND)
	img.Clear()
	img.SetColor(FOREGROUND)

	// main content text
	addedSize := 0
	zoom := 1.0
	textFontSize := fontSize
	if np := len(paragraphs); !containsMedia(paragraphs) && np < 6 {
		nchars := 0
		blankLines := 0
		for _, par := range paragraphs {
			nchars += len([]rune(par))
			if par == "" {
				blankLines++
			}
		}
		largeness := math.Pow(float64(nchars), 0.60) + math.Pow(float64(np-blankLines), 1) + math.Pow(float64(blankLines), 0.7)
		zoom = float64(math.Pow(float64(height)/366.0-(float64(blankLines+1)/10), 1.2))
		addedSize = int(200.0 / largeness * zoom)
		textFontSize = int(float64(fontSize + addedSize))
	}
	textImg, overflowingText := drawParagraphs(ctx,
		paragraphs, textFontSize, width-paddingLeft*2, height-20-barHeight)
	img.DrawImage(textImg, paddingLeft, 20)

	// font for writing the date
	img.SetFontFace(truetype.NewFace(dateFont, &truetype.Options{
		Size:    (6 * barScale),
		DPI:     260,
		Hinting: xfont.HintingFull,
	}))

	// black bar at the bottom
	img.SetColor(BAR_BACKGROUND)
	img.DrawRectangle(0, float64(height-barHeight), float64(width), float64(barHeight))
	img.Fill()

	// a rectangle at the bottom with a gradient from black to transparent
	if overflowingText {
		gradientRectY := height - barHeight - gradientRectHeight
		for y := 0; y < gradientRectHeight; y++ {
			alpha := uint8(255 * (math.Pow(float64(y)/float64(gradientRectHeight), 2)))
			img.SetRGBA255(int(BACKGROUND.R), int(BACKGROUND.G), int(BACKGROUND.B), int(alpha))
			img.DrawRectangle(0, float64(gradientRectY+y), float64(width), 1)
			img.Fill()
		}
	}

	// draw author's name
	authorTextX := paddingLeft
	picHeight := barHeight - 20
	if metadata.Picture != "" {
		authorImage, err := fetchImageFromURL(ctx, metadata.Picture)
		if err == nil {
			resizedAuthorImage := resize.Resize(uint(barHeight-20), uint(picHeight), roundImage(cropToSquare(authorImage)), resize.Lanczos3)
			img.DrawImage(resizedAuthorImage, paddingLeft, height-barHeight+10)
		}
	}

	authorTextY := height - barHeight + (barHeight-picHeight)/2 + 4

	if style == StyleFacebook {
		authorTextY = height - barHeight + (barHeight-picHeight)/2 - 5
		authorTextX += 25
	} else {
		authorTextX += 65
	}

	img.SetColor(color.White)
	textImg, _ = drawParagraphs(ctx, []string{metadata.ShortName()}, fontSize, width, barHeight)
	img.DrawImage(textImg, authorTextX, authorTextY)

	// a gradient to cover too long names
	authorMaxWidth := width/2.0 - paddingLeft*2
	img.SetColor(BAR_BACKGROUND)
	img.DrawRectangle(float64(paddingLeft+authorTextX+authorMaxWidth), float64(height-barHeight), float64(width-authorTextX-authorMaxWidth), float64(barHeight))
	gradientLength := 60
	for x := 0; x < gradientLength; x++ {
		alpha := uint8(255 - 255*(math.Pow(float64(x)/float64(gradientLength), 2)))
		img.SetRGBA255(int(BAR_BACKGROUND.R), int(BAR_BACKGROUND.G), int(BAR_BACKGROUND.B), int(alpha))
		img.DrawRectangle(float64(paddingLeft+authorTextX+authorMaxWidth-x), float64(height-barHeight), 1, float64(barHeight))
		img.Fill()
	}

	// bottom bar logo
	logo, _ := static.ReadFile("static/logo.png")
	stampImg, _ := png.Decode(bytes.NewBuffer(logo))
	stampRatio := float64(stampImg.Bounds().Dx() / stampImg.Bounds().Dy())
	stampHeight := float64(barHeight) * 0.45
	stampWidth := stampHeight * stampRatio
	resizedStampImg := resize.Resize(uint(stampWidth), uint(stampHeight), stampImg, resize.Lanczos3)
	stampX := width - int(stampWidth) - paddingLeft
	stampY := height - barHeight + (barHeight-int(stampHeight))/2
	img.DrawImage(resizedStampImg, stampX, stampY)

	// draw event date
	formattedDate := date.Format("Jan 02, 2006")
	img.SetColor(color.RGBA{160, 160, 160, 255})
	img.DrawStringWrapped(formattedDate, float64(width-paddingLeft-int(stampWidth)-250), float64(height-barHeight+(barHeight-int(stampHeight))/2)+3, 0, 0, float64(240), 1.5, gg.AlignRight)

	return img.Image(), nil
}

func drawParagraphs(ctx context.Context, paragraphs []string, fontSize int, width, height int) (image.Image, bool) {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))

	lineNumber := 1
	yPos := fontSize * lineNumber * 12 / 10
	for i := 0; i < len(paragraphs); i++ {
		paragraph := paragraphs[i]

		if paragraph == "" {
			// do not draw lines if the next element is an image
			if len(paragraphs) > i+1 && isMediaURL(paragraphs[i+1]) {
				continue
			} else {
				// just move us down a little then jump to the next line
				lineNumber++
				yPos = yPos + fontSize*12/10
				continue
			}
		}

		if isMediaURL(paragraph) {
			if i == 0 {
				yPos = 0
			}
			next := drawMediaAt(ctx, img, paragraph, yPos)
			if next != -1 {
				yPos = next
				// this means the media picture was successfully drawn
				continue
			}

			// if we reach here that means we didn't draw anything, so proceed to
			// draw the text
		}

		rawText := []rune(paragraph)

		shapedRunes, emojiMask, hlMask := shapeText(rawText, fontSize)

		var wrapper shaping.LineWrapper
		it := shaping.NewSliceIterator([]shaping.Output{shapedRunes})
		lines, _ := wrapper.WrapParagraph(shaping.WrapConfig{}, width, rawText, it)

		totalCharsWritten := 0
		for _, line := range lines {
			for _, out := range line { // this iteration is useless because there is always just one line
				charsWritten, _ := drawShapedBlockAt(
					img,
					fontSize,
					[4]color.Color{
						color.RGBA{R: 255, G: 230, B: 238, A: 255}, // normal
						color.RGBA{R: 242, G: 211, B: 152, A: 255}, // links
						color.RGBA{R: 227, G: 42, B: 109, A: 255},  // mentions -> Tailwind strongpink
						color.RGBA{R: 151, G: 210, B: 251, A: 255}, // hashtags
					},
					out,
					emojiMask,
					hlMask,
					totalCharsWritten,
					0,
					yPos,
				)
				totalCharsWritten += charsWritten

				if fontSize*lineNumber*12/10 > height {
					return img, true
				}
				lineNumber++
				yPos = yPos + fontSize*12/10
			}
		}
	}

	return img, false
}

// drawGolfScorecardImage generates a custom golf scorecard image optimized for social media sharing
func drawGolfScorecardImage(
	ctx context.Context,
	golfData Kind1501Metadata,
	metadata sdk.ProfileMetadata,
	date time.Time,
) (image image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic while drawing golf scorecard image")
			log.Warn().Interface("r", r).Msg("panic while drawing golf scorecard image")
		}
	}()

	// Social media optimized dimensions (1200x630px)
	width := 1200
	height := 630
	
	img := gg.NewContext(width, height)
	
	// Golf-themed gradient background (teal to lighter teal - matching Noga's color scheme)
	gradient := gg.NewLinearGradient(0, 0, 0, float64(height))
	gradient.AddColorStop(0, color.RGBA{27, 138, 156, 255})   // Noga Primary: #1B8A9C
	gradient.AddColorStop(1, color.RGBA{77, 182, 172, 255})   // Noga Primary Light: #4DB6AC
	img.SetFillStyle(gradient)
	img.DrawRectangle(0, 0, float64(width), float64(height))
	img.Fill()
	
	// White scorecard container
	cardMargin := 60.0
	cardWidth := float64(width) - (cardMargin * 2)
	cardHeight := float64(height) - (cardMargin * 2)
	cardX := cardMargin
	cardY := cardMargin
	
	img.SetColor(color.White)
	img.DrawRoundedRectangle(cardX, cardY, cardWidth, cardHeight, 20)
	img.Fill()
	
	// Add subtle shadow
	img.SetRGBA255(0, 0, 0, 30)
	img.DrawRoundedRectangle(cardX+5, cardY+5, cardWidth, cardHeight, 20)
	img.Fill()
	
	// Draw white card again on top
	img.SetColor(color.White)
	img.DrawRoundedRectangle(cardX, cardY, cardWidth, cardHeight, 20)
	img.Fill()
	
	// Set up fonts
	titleFont := truetype.NewFace(dateFont, &truetype.Options{
		Size:    48,
		DPI:     72,
		Hinting: xfont.HintingFull,
	})
	
	courseFont := truetype.NewFace(dateFont, &truetype.Options{
		Size:    36,
		DPI:     72,
		Hinting: xfont.HintingFull,
	})
	
	scoreFont := truetype.NewFace(dateFont, &truetype.Options{
		Size:    72,
		DPI:     72,
		Hinting: xfont.HintingFull,
	})
	
	detailFont := truetype.NewFace(dateFont, &truetype.Options{
		Size:    24,
		DPI:     72,
		Hinting: xfont.HintingFull,
	})
	
	// Title
	img.SetFontFace(titleFont)
	img.SetColor(color.RGBA{27, 138, 156, 255}) // Noga Primary: #1B8A9C
	titleText := "Golf Scorecard"
	titleWidth, _ := img.MeasureString(titleText)
	img.DrawString(titleText, (float64(width)-titleWidth)/2, cardY+80)
	
	// Course name
	img.SetFontFace(courseFont)
	img.SetColor(color.RGBA{31, 41, 55, 255}) // Gray-800
	courseName := golfData.CourseName
	if courseName == "" {
		courseName = "Golf Course"
	}
	courseWidth, _ := img.MeasureString(courseName)
	img.DrawString(courseName, (float64(width)-courseWidth)/2, cardY+140)
	
	// Score section (combined score and par difference on one line)
	img.SetFontFace(scoreFont)
	
	// Create combined score text like "71 (-1)" or "72 (E)"
	var parText string
	if golfData.ScoreToPar > 0 {
		parText = fmt.Sprintf("+%d", golfData.ScoreToPar)
	} else if golfData.ScoreToPar < 0 {
		parText = fmt.Sprintf("%d", golfData.ScoreToPar)
	} else {
		parText = "E"
	}
	
	combinedScoreText := fmt.Sprintf("%d (%s)", golfData.TotalScore, parText)
	combinedScoreWidth, _ := img.MeasureString(combinedScoreText)
	
	// Score color based on par (using Noga's golf colors)
	if golfData.ScoreToPar < 0 {
		img.SetColor(color.RGBA{76, 175, 80, 255}) // Noga Birdie: #4CAF50
	} else if golfData.ScoreToPar > 0 {
		img.SetColor(color.RGBA{244, 67, 54, 255}) // Noga Double Bogey: #F44336
	} else {
		img.SetColor(color.RGBA{30, 136, 229, 255}) // Noga Eagle: #1E88E5
	}
	
	img.DrawString(combinedScoreText, (float64(width)-combinedScoreWidth)/2, cardY+250)
	
	// Player and date info
	img.SetFontFace(detailFont)
	img.SetColor(color.RGBA{107, 114, 128, 255}) // Gray-500
	
	playerText := fmt.Sprintf("Player: %s", metadata.ShortName())
	img.DrawString(playerText, cardX+40, cardY+380)
	
	dateText := fmt.Sprintf("Date: %s", date.Format("Jan 02, 2006"))
	if golfData.Date != "" {
		dateText = fmt.Sprintf("Date: %s", golfData.Date)
	}
	img.DrawString(dateText, cardX+40, cardY+420)
	
	if golfData.Par > 0 {
		parInfoText := fmt.Sprintf("Par: %d", golfData.Par)
		img.DrawString(parInfoText, cardX+40, cardY+460)
	}
	
	// NOGA branding (positioned within card boundaries)
	img.SetColor(color.RGBA{27, 138, 156, 255}) // Noga Primary: #1B8A9C
	brandText := "gambit.golf"
	brandWidth, _ := img.MeasureString(brandText)
	img.DrawString(brandText, cardX+cardWidth-brandWidth-40, cardY+cardHeight-40)
	
	return img.Image(), nil
}
