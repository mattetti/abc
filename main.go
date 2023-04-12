package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/mattetti/m3u8Grabber/m3u8"
	"github.com/mattetti/mpdgrabber"
)

var (
	debugFlag  = flag.Bool("debug", false, "Set debug mode")
	dlAllFlag  = flag.Bool("all", false, "Download all episodes if the page contains multiple videos.")
	subsOnly   = flag.Bool("subsOnly", false, "Only download the subtitles.")
	URLFlag    = flag.String("url", "", "URL of the page to backup.")
	hlsFlag    = flag.Bool("m3u8", true, "Should use HLS/m3u8 format to download (instead of dash)")
	urlPattern = `https?://iview\.abc\.net\.au/(?:[^/]+/)*video/(?P<id>[^/?#]+)`
)

var ErrNoPlayerData = errors.New("no playerData found")
var ErrMissingPayerJSONData = errors.New("no JSON data found")
var ErrBadPlayerJSONData = errors.New("Bad JSON data found")

func main() {
	flag.Parse()
	if *URLFlag == "" {
		fmt.Println("you need to pass the URL of an abc.net.au episode page.")
		fmt.Println("For instance https://iview.abc.net.au/video/NU2314H073S00")
		os.Exit(1)
	}
	if *debugFlag {
		m3u8.Debug = true
		mpdgrabber.Debug = true
	}

	if *subsOnly {
		fmt.Println("Downloading subtitles only")
	}

	givenURL := *URLFlag

	re := regexp.MustCompile(urlPattern)
	match := re.FindStringSubmatch(givenURL)

	var videoID string
	if len(match) > 0 {
		videoID = match[re.SubexpIndex("id")]
	}
	if len(videoID) == 0 {
		fmt.Println("Could not find a video ID in the URL, this url doesn't seem valid")
		os.Exit(1)
	}

	u, err := url.Parse(givenURL)
	if err != nil {
		fmt.Println("Something went wrong when trying to parse", givenURL)
		fmt.Println(err)
		os.Exit(1)
	}

	if *debugFlag {
		fmt.Println("Checking", u)
	}

	w := &sync.WaitGroup{}
	stopChan := make(chan bool)
	// if *hlsFlag {
	// start the m3u8 workers
	m3u8.LaunchWorkers(w, stopChan)
	// } else {
	// 	mpdgrabber.LaunchWorkers(w, stopChan)
	// }

	// if *hlsFlag {
	downloadHLSVideo(givenURL, videoID)
	// } else {
	// 	downloadDashVideo(givenURL, videoID)
	// }

	if *hlsFlag {
		close(m3u8.DlChan)
		w.Wait()
	}
	// else {
	// 	mpdgrabber.Close()
	// 	w.Wait()
	// }
}

func downloadHLSVideo(givenURL, videoID string) {
	info, err := fetchStreamInfo(givenURL, videoID)
	if err != nil {
		fmt.Println("Something went wrong when trying to fetch the stream info")
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println(info.SeriesTitle + " - " + info.Title)
	var streamURL string
	for _, playlist := range info.Playlists {
		if playlist.Type == "program" {
			if len(playlist.Streams.Hls.HD) > 0 {
				streamURL = playlist.Streams.Hls.HD
				break
			}
			if len(playlist.Streams.Hls.Sd) > 0 {
				streamURL = playlist.Streams.Hls.Sd
				break
			}
			if len(playlist.Streams.Hls.SdLow) > 0 {
				streamURL = playlist.Streams.Hls.SdLow
				break
			}
		}
	}
	if len(streamURL) == 0 {
		fmt.Println("Could not find a stream URL")
		os.Exit(1)
	}

	ID := info.EpisodeHouseNumber
	ts := strconv.Itoa(int(time.Now().Unix()))
	path := fmt.Sprintf("/auth/hls/sign?ts=%s&hn=%s&d=android-tablet", ts, ID)
	sig := hmac.New(sha256.New, []byte("android.content.res.Resources"))
	sig.Write([]byte(path))
	sigBytes := sig.Sum(nil)
	sigStr := fmt.Sprintf("%x", sigBytes)
	reqURL := fmt.Sprintf("http://iview.abc.net.au%s&sig=%s", path, sigStr)

	req, err := buildRequest(reqURL, givenURL)
	if err != nil {
		log.Fatalf("failed to build request to %s, err: %v", reqURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("failed to send request to %s, err: %v", reqURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Fatalf("failed to download %s, status code: %d", reqURL, resp.StatusCode)
	}

	tokenBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("failed to read response body, err: %v", err)
	}
	token := string(tokenBytes)
	manifestURL := fmt.Sprintf("%s?hdnea=%s", streamURL, token)

	filename := fmt.Sprintf("%s - %s", info.SeriesTitle, info.Title)
	pathToUse, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	destPath := filepath.Join(pathToUse, filename+".mp4")
	if fileAlreadyExists(destPath) {
		fmt.Printf("%s already exists\n", destPath)
		return
	}

	if *debugFlag {
		fmt.Println(manifestURL)
	}

	job := &m3u8.WJob{
		Type:     m3u8.ListDL,
		URL:      manifestURL,
		SubsOnly: *subsOnly,
		// SkipConverter: true,
		DestPath: pathToUse,
		Filename: filename}
	m3u8.DlChan <- job
}

func fetchStreamInfo(originURL, videoID string) (*StreamData, error) {

	reqURL := fmt.Sprintf("https://iview.abc.net.au/api/programs/%s", videoID)

	req, err := buildRequest(reqURL, originURL)
	if err != nil {
		return nil, fmt.Errorf("failed to build request to %s, err: %v", reqURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request to %s, err: %v", reqURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to download %s, status code: %d", reqURL, resp.StatusCode)
	}

	var stream StreamData
	err = json.NewDecoder(resp.Body).Decode(&stream)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON response data\nerr: %v", err)
	}
	return &stream, nil
}

func buildRequest(reqURL, referrer string) (*http.Request, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request for %s, err: %v", reqURL, err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Dnt", "1")
	req.Header.Set("Origin", "https://iview.abc.net.au/")
	req.Header.Set("Referer", referrer)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/108.0.0.0 Safari/537.36")
	req.Header.Set("Sec-Ch-Ua", "Chromium\";v=\"108\", \"Google Chrome\";v=\"108\"")
	req.Header.Set("Sec-Ch-Ua-Platform", "\"macOS\"")

	return req, nil
}

func fileAlreadyExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

type StreamData struct {
	ShowID             string `json:"showID"`
	SeriesTitle        string `json:"seriesTitle"`
	Title              string `json:"title"`
	Href               string `json:"href"`
	Format             string `json:"format"`
	FormatBgColour     string `json:"formatBgColour"`
	FormatTextColour   string `json:"formatTextColour"`
	Channel            string `json:"channel"`
	ChannelTitle       string `json:"channelTitle"`
	PubDate            string `json:"pubDate"`
	Thumbnail          string `json:"thumbnail"`
	EpisodeHouseNumber string `json:"episodeHouseNumber"`
	Duration           string `json:"duration"`
	Rating             string `json:"rating"`
	Label              string `json:"label"`
	OztamPublisherID   string `json:"oztamPublisherID"`
	ExpireDate         string `json:"expireDate"`
	SeriesHouseNumber  string `json:"seriesHouseNumber"`
	Categories         []struct {
		Title string `json:"title"`
		Href  string `json:"href"`
	} `json:"categories"`
	Keywords     string `json:"keywords"`
	Description  string `json:"description"`
	Related      string `json:"related"`
	EpisodeCount int    `json:"episodeCount"`
	Availability string `json:"availability"`
	Playlists    []struct {
		Type         string `json:"type"`
		StreamLabels struct {
			Sd    string `json:"sd"`
			SdLow string `json:"sd-low"`
		} `json:"stream-labels"`
		Streams struct {
			Mpegdash struct {
				Sd        string `json:"sd"`
				SdLow     string `json:"sd-low"`
				Protected bool   `json:"protected"`
			} `json:"mpegdash"`
			Fairplay struct {
				Sd        string `json:"sd"`
				SdLow     string `json:"sd-low"`
				Protected bool   `json:"protected"`
			} `json:"fairplay"`
			Hls struct {
				HD        string `json:"720"`
				Sd        string `json:"sd"`
				SdLow     string `json:"sd-low"`
				Protected bool   `json:"protected"`
			} `json:"hls"`
		} `json:"streams"`
		HlsPlus  string `json:"hls-plus"`
		HlsHigh  string `json:"hls-high"`
		HlsBase  string `json:"hls-base"`
		HlsLow   string `json:"hls-low"`
		Captions struct {
			SrcVtt string `json:"src-vtt"`
			Live   string `json:"live"`
		} `json:"captions,omitempty"`
	} `json:"playlist"`
	Captions bool `json:"captions"`
	Streams  struct {
		HlsBase []string `json:"hls-base"`
		HlsHigh []string `json:"hls-high"`
		HlsLow  []string `json:"hls-low"`
	} `json:"streams"`
	Share       string `json:"share"`
	NextEpisode struct {
		CuePoint    int    `json:"cuePoint"`
		Href        string `json:"href"`
		SeriesTitle string `json:"seriesTitle"`
		Title       string `json:"title"`
	} `json:"nextEpisode"`
}
