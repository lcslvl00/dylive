package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/caiguanhao/dylive"
)

var (
	currentRooms = map[string]string{}
	pids         = map[string]int{}

	preferQuality, preferFormat string
	outputJson                  bool
	commadnTemplate             string
	checkCommand                bool
	tikHubToken                 string
)

func main() {
	flag.StringVar(&preferQuality, "q", "", "video quality (uhd, hd, ld, sd)")
	flag.StringVar(&preferFormat, "f", "flv", "format (flv, hls, m3u8)")
	flag.BoolVar(&outputJson, "json", false, "output json instead of url")
	flag.StringVar(&commadnTemplate, "run", "", "command template to run; use @/path/to/template.sh to specify a template file")
	flag.BoolVar(&checkCommand, "check", false, "re-run command if process does not exist")
	flag.StringVar(&tikHubToken, "thtoken", "", "TikHub token")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Monitor live streams from Douyin.")
		fmt.Fprintln(flag.CommandLine.Output())
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	for {
		getRoom()
	}
}

func sleepRoomNotActive() {

	sleepDurationMin := 1
	nowHour := time.Now().Local().Hour()
	if nowHour >= 5 && nowHour <= 12 { // 5am - 12pm
		sleepDurationMin = 1
	} else if nowHour > 12 && nowHour < 19 { // 12pm - 7pm
		sleepDurationMin = 5
	} else {
		sleepDurationMin = 5
	}
	log.Printf("Current time is %d sleeping for %d minutes", nowHour, sleepDurationMin)
	time.Sleep(time.Duration(sleepDurationMin) * time.Minute)
}

func getRoom() {
	ids := flag.Args()
	cookies := ""
	if len(ids) == 0 {
		log.Println("At least one Douyin ID is required.")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, userId := range ids {

		if checkCommand && pids[userId] > 0 {

			if isProcessRunning(pids[userId]) {
				//log.Println("PID active; skipping live room query")
				time.Sleep(5 * time.Second)
				continue
			} else {
				log.Println("Process", pids[userId], "exited; check to see if room still active")
			}
		}

		room, err := dylive.GetRoom(ctx, userId, cookies)
		if err == dylive.ErrCaptchaRequested {
			cookies, err = dylive.GetGuestCookieTikHub(tikHubToken)
			if err != nil {
				log.Println("Error retreiving guest cookies")
			}
			sleepRoomNotActive()
		}

		//room, err := dylive.GetRoomTikHub(ctx, userId, tikHubToken)
		if err != nil || room == nil {
			log.Println(err)
			delete(pids, userId)

			sleepRoomNotActive()
			continue
		}

		currentRooms[userId] = userId
		if room.StatusCode != dylive.RoomStatusLiveOn {
			log.Printf("%s (%s) hasn't started livestream yet.", room.User.Name, room.DouyinId)
			delete(pids, userId)

			sleepRoomNotActive()
			continue
		}
		log.Printf("%s (%s) is live.", room.User.Name, room.DouyinId)
		updateStreamUrl(room)
		if outputJson {
			json.NewEncoder(os.Stdout).Encode(room)
		} else {
			fmt.Println(room.StreamUrl)
		}
		if commadnTemplate != "" {
			if err := runCommand(commadnTemplate, room, userId); err != nil {
				log.Println(err)
			}
		}
	}
}

func updateStreamUrl(room *dylive.Room) {
	if preferFormat == "hls" || preferFormat == "m3u8" {
		room.StreamUrl = room.HlsUrlForQuality(preferQuality)
	} else {
		room.StreamUrl = room.FlvUrlForQuality(preferQuality)
	}
}

func runCommand(tpl string, room *dylive.Room, userId string) error {
	if len(tpl) > 1 && strings.HasPrefix(tpl, "@") {
		content, _ := os.ReadFile(tpl[1:])
		tpl = string(content)
	}
	if tpl == "" {
		return nil
	}
	tmpl, err := template.New("").Parse(tpl)
	if err != nil {
		return err
	}
	var cmdStrBuilder strings.Builder
	err = tmpl.Execute(&cmdStrBuilder, struct {
		*dylive.Room
		Timestamp int64
	}{
		Room:      room,
		Timestamp: time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	cmdStr := cmdStrBuilder.String()
	if cmdStr == "" {
		return nil
	}
	cmd := exec.Command("sh", "-c", cmdStr)
	err = cmd.Start()
	if err == nil {
		log.Println("Command", cmdStr, "started as PID", cmd.Process.Pid)
		pids[userId] = cmd.Process.Pid
		go func() {
			err := cmd.Wait()
			if err != nil {
				log.Printf("Process %d exited with error: %s\n", cmd.Process.Pid, err)
			} else {
				log.Printf("Process %d exited successfully\n", cmd.Process.Pid)
			}
		}()
	}
	return err
}

func isProcessRunning(pid int) bool {
	// PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	handle, err := syscall.OpenProcess(0x1000, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	return true
}
