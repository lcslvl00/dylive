package dylive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"

	netscapecookiejar "github.com/vanym/golang-netscape-cookiejar"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:109.0) Gecko/20100101 Firefox/115.0"

var ErrCaptchaRequested = errors.New("captcha requested")
var ErrGuestCookieRetreivalFailed = errors.New("guest cookie retieve failure")

type (
	Category struct {
		Id         string
		Name       string
		Categories []Category
	}

	dyCategory struct {
		Partition struct {
			IDStr string `json:"id_str"`
			Type  int    `json:"type"`
			Title string `json:"title"`
		} `json:"partition"`
		SubPartition []dyCategory `json:"sub_partition"`
	}

	dyliveCategories struct {
		CategoryData []dyCategory `json:"categoryData"`
	}
)

// GetCategories gets all Douyin live stream categories.
func GetCategories(ctx context.Context) ([]Category, error) {
	const first = "4_101"
	var categories []Category
	data, err := getCategoryPageData(ctx, first, "categoryData")
	if err != nil {
		return nil, err
	}
	var cats dyliveCategories
	if err := getDataInArray(data[0], &cats); err != nil {
		return nil, err
	}
	categories = append(categories, deepConvertDyCategories(cats.CategoryData, nil)...)
	return categories, nil
}

func deepConvertDyCategories(in []dyCategory, whitelist []string) []Category {
	out := []Category{}
	for _, c := range in {
		if cat := convertDyCategory(c, nil, 0, whitelist); cat != nil {
			out = append(out, *cat)
		}
	}
	return out
}

func convertDyCategory(c dyCategory, parentIDParts []string, level int, whitelist []string) *Category {
	id := fmt.Sprintf("%d_%s", c.Partition.Type, c.Partition.IDStr)
	if len(whitelist) > 0 && (level >= len(whitelist) || whitelist[level] != id) {
		return nil
	}
	idParts := append([]string{}, parentIDParts...)
	idParts = append(idParts, id)
	fullId := strings.Join(idParts, "_")
	children := []Category{}
	for _, sub := range c.SubPartition {
		if cat := convertDyCategory(sub, idParts, level+1, whitelist); cat != nil {
			children = append(children, *cat)
		}
	}
	return &Category{
		Id:         fullId,
		Name:       c.Partition.Title,
		Categories: children,
	}
}

const (
	RoomStatusLiveOn RoomStatus = 2 + iota
	_
	RoomStatusLiveOff
)

type (
	RoomStatus = int

	Room struct {
		Id                string
		DouyinId          string
		StatusCode        RoomStatus
		Name              string
		CoverUrl          string
		WebUrl            string
		CurrentUsersCount string
		TotalUsersCount   string
		Category          *Category
		User              User
		StreamUrl         string
		FlvStreamUrls     map[string]string
		HlsStreamUrls     map[string]string
	}

	User struct {
		Name    string
		Picture string
	}

	dyUser struct {
		Nickname    string `json:"nickname"`
		AvatarThumb struct {
			UrlList []string `json:"url_list"`
		} `json:"avatar_thumb"`
	}

	dyliveRoom struct {
		IdStr  string `json:"id_str"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Cover  struct {
			UrlList []string `json:"url_list"`
		} `json:"cover"`
		Stats struct {
			TotalUserStr string `json:"total_user_str"`
			UserCountStr string `json:"user_count_str"`
		} `json:"stats"`
		Owner     dyUser `json:"owner"`
		StreamUrl struct {
			FlvPullUrl        map[string]string `json:"flv_pull_url"`
			HlsPullUrlMap     map[string]string `json:"hls_pull_url_map"`
			DefaultResolution string            `json:"default_resolution"`
		} `json:"stream_url"`
		RoomViewStats struct {
			DisplayValue int `json:"display_value"`
		} `json:"room_view_stats"`
	}

	dyliveCategory struct {
		RoomsData struct {
			Data []struct {
				Room      dyliveRoom `json:"room"`
				WebRid    string     `json:"web_rid"`
				StreamSrc string     `json:"streamSrc"`
				Cover     string     `json:"cover"`
				Avatar    string     `json:"avatar"`
			} `json:"data"`
		} `json:"roomsData"`
		CategoryData []dyCategory `json:"categoryData"`
		CategoryList []string     `json:"categoryList"`
	}
)

// FlvUrlForQuality returns the .flv stream URL for the given quality (uhd, hd, ld, sd).
// If no matching URL is found, it returns the room's default StreamUrl.
func (room Room) FlvUrlForQuality(quality string) string {
	return room.urlForQuality(room.FlvStreamUrls, quality)
}

// HlsUrlForQuality returns the .m3u8 stream URL for the given quality (uhd, hd, ld, sd).
// If no matching URL is found, it returns the room's default StreamUrl.
func (room Room) HlsUrlForQuality(quality string) string {
	return room.urlForQuality(room.HlsStreamUrls, quality)
}

func (room Room) urlForQuality(urls map[string]string, quality string) string {
	quality = strings.ToLower(quality)
	for key, value := range urls {
		switch quality {
		case "uhd":
			if strings.Contains(key, "FULL_HD") || strings.Contains(value, "_uhd") {
				log.Println("Returning UHD quality")
				return value
			}
		case "hd":
			if strings.Contains(value, "_hd") {
				return value
			}
		case "ld":
			if strings.Contains(value, "_ld") {
				return value
			}
		case "sd":
			if strings.Contains(value, "_sd") {
				return value
			}
		default:
			return room.StreamUrl
		}
	}
	log.Println("Returning default quality!")
	return room.StreamUrl
}

// GetRoomsByCategory gets top 15 Douyin live stream rooms of a category.
func GetRoomsByCategory(ctx context.Context, categoryId string) ([]Room, error) {
	data, err := getCategoryPageData(ctx, categoryId, "roomsData")
	if err != nil {
		return nil, err
	}
	roomsData := data[0]
	var cat dyliveCategory
	if err := getDataInArray(roomsData, &cat); err != nil {
		return nil, err
	}
	var category *Category
	if categories := deepConvertDyCategories(cat.CategoryData, cat.CategoryList); len(categories) > 0 {
		category = &categories[0]
	}

	var rooms []Room
	for _, room := range cat.RoomsData.Data {
		var count string
		if room.Room.RoomViewStats.DisplayValue > 0 {
			count = strconv.Itoa(room.Room.RoomViewStats.DisplayValue)
		} else {
			count = room.Room.Stats.UserCountStr
		}
		rooms = append(rooms, Room{
			Id:                room.Room.IdStr,
			DouyinId:          room.WebRid,
			StatusCode:        RoomStatusLiveOn,
			Name:              room.Room.Title,
			CoverUrl:          room.Cover,
			WebUrl:            "https://live.douyin.com/" + room.WebRid,
			StreamUrl:         room.StreamSrc,
			FlvStreamUrls:     room.Room.StreamUrl.FlvPullUrl,
			HlsStreamUrls:     room.Room.StreamUrl.HlsPullUrlMap,
			CurrentUsersCount: count,
			TotalUsersCount:   room.Room.Stats.TotalUserStr,
			Category:          category,
			User: User{
				Name:    room.Room.Owner.Nickname,
				Picture: room.Avatar,
			},
		})
	}
	return rooms, nil
}

func getCategoryPageData(ctx context.Context, id string, filters ...string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://live.douyin.com/categorynew/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	parts := getDataInHtml(string(b))
	var output []string
	for _, filter := range filters {
		var ret string
		for _, part := range parts {
			if strings.Contains(part, filter) {
				ret = part
				break
			}
		}
		output = append(output, ret)
	}
	return output, nil
}

type (
	dyliveRoomDetails struct {
		State struct {
			RoomStore struct {
				RoomInfo struct {
					Room   dyliveRoom `json:"room"`
					WebRid string     `json:"web_rid"`
					Anchor dyUser     `json:"anchor"`
				} `json:"roomInfo"`
			} `json:"roomStore"`
		} `json:"state"`
	}
)

type StreamURL struct {
	FlvPullURL map[string]string `json:"flv_pull_url"`
}

type Data struct {
	IDStr        string `json:"id_str"`
	Status       int    `json:"status"`
	StatusStr    string `json:"status_str"`
	Title        string `json:"title"`
	UserCountStr string `json:"user_count_str"`
	Cover        struct {
		URLList []string `json:"url_list"`
	} `json:"cover"`
	StreamURL struct {
		FlvPullURL        map[string]string `json:"flv_pull_url"`
		HlsPullURLMap     map[string]string `json:"hls_pull_url_map"`
		HlsPullURL        string            `json:"hls_pull_url"`
		DefaultResolution string            `json:"default_resolution"`
		StreamOrientation int               `json:"stream_orientation"`
	} `json:"stream_url"`
}

type Response struct {
	OuterData struct {
		OuterData2 struct {
			Data []Data `json:"data"`
		} `json:"data"`
	} `json:"data"`
}

type CookieResponse struct {
	Data struct {
		Cookie string `json:"Cookie"`
	} `json:"data"`
}

func GetGuestCookieTikHub(tikhubToken string) (string, error) {

	url := "https://api.tikhub.io/api/v1/douyin/web/fetch_douyin_web_guest_cookie?user_agent=" + url.QueryEscape("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.212 Safari/537.36")
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, url, nil)

	if err != nil {
		fmt.Println(err)
		return "", err
	}
	req.Header.Add("Authorization", "Bearer "+tikhubToken)

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return "", err
	}

	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return "", err
	}

	var response CookieResponse
	err = json.Unmarshal([]byte(body), &response)
	if err != nil {
		log.Println("Error deserializing tikhub JSON. HTTP response code was " + res.Status + " err is " + err.Error() + " response: " + string(body))
		return "", err
	}

	cookie := response.Data.Cookie
	if len(cookie) == 0 {
		log.Println("Error retreiving guest cookie: " + string(body))
		return "", ErrGuestCookieRetreivalFailed
	}
	return cookie, nil
}

func GetRoomTikHub(ctx context.Context, douyinId string, tikhubToken string) (*Room, error) {

	url := "https://api.tikhub.io/api/v1/douyin/web/fetch_user_live_videos?webcast_id=" + douyinId
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, url, nil)

	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer "+tikhubToken)

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	var response Response
	err = json.Unmarshal([]byte(body), &response)
	if err != nil {
		log.Println("Error deserializing tikhub JSON. HTTP response code was " + res.Status + " err is " + err.Error() + " response: " + string(body))
		return nil, err
	}

	if len(response.OuterData.OuterData2.Data) > 0 {

		d := response.OuterData.OuterData2.Data[0]

		return &Room{
			Id:                d.IDStr,
			DouyinId:          douyinId,
			StatusCode:        d.Status,
			Name:              d.Title,
			CoverUrl:          "",
			WebUrl:            "https://live.douyin.com/" + douyinId,
			StreamUrl:         "",
			FlvStreamUrls:     d.StreamURL.FlvPullURL,
			HlsStreamUrls:     d.StreamURL.HlsPullURLMap,
			CurrentUsersCount: d.UserCountStr,
			TotalUsersCount:   d.UserCountStr,
			User: User{
				Name:    "user",
				Picture: "picture",
			},
		}, nil
	}

	log.Println("Error interpreting tikhub response: " + string(body))

	return nil, err
}

// GetRoom get live stream room details by Douyin ID (抖音号)
func GetRoom(ctx context.Context, douyinId string, cookies string) (*Room, error) {
	data, err := getLivePageData(ctx, douyinId, cookies, "flv_pull_url")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[0] == "" {
		return nil, fmt.Errorf("!!DouyinId %s does not exist", douyinId)
	}
	roomsData := data[0]
	var page dyliveRoomDetails
	if err := getDataInArray(roomsData, &page); err != nil {
		return nil, err
	}

	info := page.State.RoomStore.RoomInfo

	var cover string
	if len(info.Room.Cover.UrlList) > 0 {
		cover = info.Room.Cover.UrlList[0]
	}

	streamUrl := info.Room.StreamUrl.FlvPullUrl[info.Room.StreamUrl.DefaultResolution]

	var count string
	if info.Room.RoomViewStats.DisplayValue > 0 {
		count = strconv.Itoa(info.Room.RoomViewStats.DisplayValue)
	} else {
		count = info.Room.Stats.UserCountStr
	}

	userName := info.Room.Owner.Nickname
	if userName == "" {
		userName = info.Anchor.Nickname
	}

	var userPicture string
	if len(info.Room.Owner.AvatarThumb.UrlList) > 0 {
		userPicture = info.Room.Owner.AvatarThumb.UrlList[0]
	} else if len(info.Anchor.AvatarThumb.UrlList) > 0 {
		userPicture = info.Anchor.AvatarThumb.UrlList[0]
	}

	return &Room{
		Id:                info.Room.IdStr,
		DouyinId:          info.WebRid,
		StatusCode:        info.Room.Status,
		Name:              info.Room.Title,
		CoverUrl:          cover,
		WebUrl:            "https://live.douyin.com/" + info.WebRid,
		StreamUrl:         streamUrl,
		FlvStreamUrls:     info.Room.StreamUrl.FlvPullUrl,
		HlsStreamUrls:     info.Room.StreamUrl.HlsPullUrlMap,
		CurrentUsersCount: count,
		TotalUsersCount:   info.Room.Stats.TotalUserStr,
		User: User{
			Name:    userName,
			Picture: userPicture,
		},
	}, nil
}

func getCookieJar() (http.CookieJar, error) {

	/*
		rawCookies, err := os.ReadFile("C:\\Users\\jzabl\\Downloads\\live.douyin.com_cookies.txt")
		if err != nil {
			return nil, err
		}
		cookie, err := netscapecookiejar.Unmarshal(string(rawCookies))
		if err != nil {
			return nil, err
		}

		return cookie, err
	*/

	cookiePath := "C:\\Users\\jzabl\\Downloads\\live.douyin.com_cookies.txt"

	subjar, err := cookiejar.New(&cookiejar.Options{})
	jar, err := netscapecookiejar.New(&netscapecookiejar.Options{
		SubJar:        subjar,
		AutoWritePath: cookiePath,
		WriteHeader:   true,
	})
	file, err := os.Open(cookiePath)
	_, err = jar.ReadFrom(file)
	file.Close()

	return jar, err
}

func getLivePageData(ctx context.Context, douyinId string, cookies string, filters ...string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://live.douyin.com/"+douyinId, nil)
	if err != nil {
		return nil, err
	}

	log.Println("Fetching live page data directly from douyin")

	jar, err := getCookieJar()
	if err != nil {
		log.Println("Error getting cookies from jar")
	}

	//proxyURL, _ := url.Parse("http://127.0.0.1:8866")
	//proxy := http.ProxyURL(proxyURL)
	//transport := &http.Transport{Proxy: proxy}

	client := &http.Client{
		Jar: jar,
		//Transport: transport,
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cookie", cookies)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	parts := getDataInHtml(string(b))
	var output []string
	for _, filter := range filters {
		var ret string
		for _, part := range parts {
			if strings.Contains(part, filter) {
				ret = part
				break
			}
		}
		output = append(output, ret)
	}

	if strings.Contains(string(b), "verify_data") {
		log.Println("Captcha requested!")
		return nil, ErrCaptchaRequested
	}

	return output, nil
}

func getDataInHtml(input string) (output []string) {
	const funcName = "__pace_f"
	const endTag = "</script>"
	var parts []string
	for {
		a := strings.Index(input, funcName)
		if a == -1 {
			break
		}
		input = input[a+len(funcName):]
		b := strings.Index(input, `"`)
		if b < 0 {
			continue
		}
		input = input[b+1:]
		b = strings.Index(input, endTag)
		if b < 0 {
			continue
		}
		b = strings.LastIndex(input[:b], `"`)
		if b < 0 {
			continue
		}
		var ret string
		if json.Unmarshal([]byte(`"`+input[:b]+`"`), &ret) != nil {
			continue
		}
		parts = append(parts, ret)
	}
	parts = strings.Split(strings.Join(parts, "\n"), "\n")
	for _, part := range parts {
		a := strings.IndexAny(part, "[{")
		if a == -1 {
			continue
		}
		b := strings.LastIndexAny(part, "}]")
		if b == -1 {
			continue
		}
		output = append(output, part[a:b+1])
	}
	return
}

func getDataInArray(input string, target interface{}) error {
	var array []interface{}
	if err := json.Unmarshal([]byte(input), &array); err != nil {
		return err
	}
	for _, element := range array {
		switch v := element.(type) {
		case map[string]interface{}:
			jsonStr, err := json.Marshal(v)
			if err != nil {
				continue
			}
			return json.Unmarshal(jsonStr, target)
		}
	}
	return nil
}
