package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"gocv.io/x/gocv"
)

var (
	templates []gocv.Mat
	inputFile string
	dryRun    bool
	verbosive bool
	debug     bool
)

type tplFileArg []string

func (t tplFileArg) String() string {
	return strings.Join(t, ", ")
}

func (t *tplFileArg) Set(s string) error {
	*t = append(*t, s)
	return nil
}

func addOptions(in []string) []string {
	mmqs := os.Getenv("MAX_MUXING_QUEUE_SIZE")
	if mmqs == "" {
		return in
	}
	return append(in, "-max_muxing_queue_size", mmqs)
}

func main() {
	var tplFiles tplFileArg
	flag.Var(&tplFiles, "t", "template file")
	flag.StringVar(&inputFile, "in", "", "input file")
	outputFile := flag.String("out", "", "output file")
	flag.BoolVar(&dryRun, "dryrun", false, "print command to stdout but don't execute them")
	flag.BoolVar(&verbosive, "verbose", false, "verbosive")
	firstStep := flag.Float64("step", 20, "first step seconds")
	ffmpegArgs := flag.String("ffmpeg", "ffmpeg -loglevel warning -y", "ffmpeg args")
	noClean := flag.Bool("noclean", false, "don't remove intermediate files")
	boxBlur := flag.String("boxblur", "20", "ffmpeg boxblur parameters, see https://ffmpeg.org/ffmpeg-filters.html#boxblur")
	timeRange := flag.String("range", "", "specify time range for the first time find, hh:mm:ss-hh:mm:ss or sec-sec")
	flag.Parse()

	debug = os.Getenv("DEBUG") == "1"

	if inputFile == "" {
		log.Fatal("please provide input file")
	}

	if *outputFile == "" {
		log.Fatal("please provide output file")
	}

	if len(tplFiles) == 0 {
		log.Fatal("please provide template files")
	}

	for _, tplFile := range tplFiles {
		template := gocv.IMRead(tplFile, gocv.IMReadGrayScale)
		if template.Empty() {
			log.Fatal("invalid template file", tplFile)
		}
		defer template.Close()
		templates = append(templates, template)
	}
	log.Println("using", len(templates), "templates")

	ffmpeg := strings.Fields(*ffmpegArgs)

	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", inputFile)
	out, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}
	var result ffprobeResult
	err = json.Unmarshal(out, &result)
	if err != nil {
		log.Fatal(err)
	}
	if verbosive {
		log.Printf("ffprobe result: %+v", result)
	}
	var videoCodec string
	for _, s := range result.Streams {
		if s.CodecType == "video" {
			videoCodec = s.CodecName
		}
	}
	if videoCodec == "" {
		log.Fatal("unknown video codec")
	}

	oldStep := *firstStep
	duration, _ := strconv.ParseFloat(result.Format.Duration, 64)

	from := 0.0
	to := duration
	tr := strings.SplitN(*timeRange, "-", 2)
	if len(tr) == 2 {
		zero, _ := time.Parse("15:04:05", "00:00:00")
		if strings.Contains(tr[0], ":") {
			if t, err := time.Parse("15:04:05", tr[0]); err == nil {
				from = t.Sub(zero).Seconds()
			}
		} else if tr[0] != "" {
			from, _ = strconv.ParseFloat(tr[0], 64)
		}
		if strings.Contains(tr[1], ":") {
			if t, err := time.Parse("15:04:05", tr[1]); err == nil {
				to = t.Sub(zero).Seconds()
			}
		} else if tr[1] != "" {
			to, _ = strconv.ParseFloat(tr[1], 64)
		}
		if from > to || from < 0 {
			log.Fatal("invalid time range")
		} else if to > duration {
			log.Fatalln("time range is over video duration", int(duration))
		}
	}

	log.Printf("scanning template for every %.1f seconds from %.1f (%s) to %.1f (%s)",
		oldStep, from, secToTime(int64(from)), to, secToTime(int64(to)))
	_, _, parts := findTemplate(generateSeries(from, to, oldStep), false)
	if len(parts) == 0 {
		log.Println("no template is found in the video")
		log.Println("all done")
		return
	}
	keep := addOptions(append(ffmpeg, "-i", inputFile))
	change := addOptions(append(ffmpeg, "-i", inputFile))
	var s float64
	var idx int
	points := []indexpoint{}
	intermediateFiles := []string{}
	filesToMerge := []string{}
	stime := []string{}
	for partNo, part := range parts {
		oldStep = *firstStep
		steps := []float64{2, 0.5, 0.1}
		a, b := part[0], part[1]
		for _, step := range steps {
			from = a.second - oldStep
			to = b.second + oldStep
			log.Printf("part#%d: scanning template for every %.1f seconds from %.1f (%s) to %.1f (%s)",
				partNo, step, from, secToTime(int64(from)), to, secToTime(int64(to)))
			a, b, _ = findTemplate(generateSeries(from, to, step), true)
			oldStep = step
		}
		keepFile := fmt.Sprintf("part-%02d.ts", idx)
		keep = append(keep,
			"-ss", fmt.Sprintf("%.2f", s),
			"-t", fmt.Sprintf("%.2f", a.second-s),
			"-codec", "copy",
			keepFile,
		)
		changeFile := fmt.Sprintf("change-%02d.ts", idx)
		intermediateFiles = append(intermediateFiles, changeFile)
		change = append(change,
			"-ss", fmt.Sprintf("%.2f", a.second),
			"-t", fmt.Sprintf("%.2f", b.second-a.second),
			"-codec", "copy",
			changeFile,
		)
		stime = append(stime, fmt.Sprintf("%.2f", a.second))
		points = append(points, indexpoint{index: idx, point: a.point})
		changedFile := fmt.Sprintf("changed-%02d.ts", idx)
		filesToMerge = append(filesToMerge, keepFile, changedFile)
		s = b.second
		stime = append(stime, fmt.Sprintf("%.2f", s))
		idx += 1
	}
	keepFile := fmt.Sprintf("part-%02d.ts", idx)
	keep = append(keep, "-ss", fmt.Sprintf("%.2f", s), "-codec", "copy", keepFile)
	filesToMerge = append(filesToMerge, keepFile)
	log.Println("splitting videos at time:", stime)
	runCommand(keep)
	runCommand(change)
	for _, p := range points {
		filter := fmt.Sprintf("[0:v]crop=%d:%d:%d:%d,boxblur=%s[fg]; [0:v][fg]overlay=%d:%d[v]",
			p.point.Width, p.point.Height, p.point.X, p.point.Y, *boxBlur, p.point.X, p.point.Y)
		cmd := append(addOptions(append(ffmpeg, "-i", fmt.Sprintf("change-%02d.ts", p.index))),
			"-filter_complex", filter,
			"-map", "[v]", "-map", "0:a",
			"-c:v", videoCodec, "-c:a", "copy",
			fmt.Sprintf("changed-%02d.ts", p.index),
		)
		log.Printf("applying (boxblur=%s) filter at (%d, %d)", *boxBlur, p.point.X, p.point.Y)
		runCommand(cmd)
	}
	log.Println("merging videos to", *outputFile)
	concat := append(addOptions(append(ffmpeg, "-i", "concat:"+strings.Join(filesToMerge, "|"))),
		"-c", "copy", *outputFile)
	runCommand(concat)
	if *noClean == false {
		intermediateFiles = append(intermediateFiles, filesToMerge...)
		for _, file := range intermediateFiles {
			if verbosive {
				log.Println("removing", file)
			}
			if !dryRun {
				err := os.Remove(file)
				if err != nil {
					log.Println("error removing file:", file, err)
				}
			}
		}
	}
	log.Println("all done")
}

type (
	ffprobeResult struct {
		Streams []struct {
			Index         int    `json:"index"`
			CodecName     string `json:"codec_name"`
			CodecLongName string `json:"codec_long_name"`
			CodecType     string `json:"codec_type"`
			Width         int    `json:"width,omitempty"`
			Height        int    `json:"height,omitempty"`
			CodedWidth    int    `json:"coded_width,omitempty"`
			CodedHeight   int    `json:"coded_height,omitempty"`
		} `json:"streams"`
		Format struct {
			FormatName     string `json:"format_name"`
			FormatLongName string `json:"format_long_name"`
			Duration       string `json:"duration"`
			Size           string `json:"size"`
			BitRate        string `json:"bit_rate"`
		} `json:"format"`
	}

	indexpoint struct {
		index int
		point *imagePoint
	}

	timepoint struct {
		second float64
		point  *imagePoint
	}

	imagePoint struct {
		Width  int
		Height int
		*image.Point
	}
)

func (tp *timepoint) String() string {
	if tp == nil {
		return "(nil)"
	}
	if debug {
		str := "&timepoint{second: " + strconv.FormatFloat(tp.second, 'f', 2, 64) + ", point: "
		if tp.point == nil {
			str += "nil"
		} else {
			str += "&image.Point{X: " + strconv.Itoa(tp.point.X) + ", Y: " + strconv.Itoa(tp.point.Y) + "}"
		}
		str += "}"
		return str
	}
	return secToTime(int64(tp.second)) +
		"@(" + strconv.Itoa(tp.point.X) + "," + strconv.Itoa(tp.point.Y) + ")"
}

// Find template image at every specified second of the video and return
// matching time ranges and image locations.  Commands are executed one by one
// from the beginning and end of the video to the middle, therefore the seconds
// must be sorted in ascending order.  At most one time range is returned when
// getOne is true.
//
// 在视频的每一指定秒数时间上查找模板图像并返回匹配的时间范围和图像位置。
// 命令是从视频的头尾向中间时间逐个执行的，所以 seconds 必须从小到大排序。
// getOne 为 true 时返回最多一个时间范围。
//
func findTemplate(seconds []float64, getOne bool) (na, nb *timepoint, parts [][]*timepoint) {
	chan1 := make(chan float64)
	chan2 := make(chan float64)
	half := len(seconds) / 2
	go func() {
		for _, second := range seconds[:half] {
			chan1 <- second
		}
		close(chan1)
	}()
	go func() {
		arr := seconds[half:]
		for i := len(arr) - 1; i > -1; i-- {
			chan2 <- arr[i]
		}
		close(chan2)
	}()
	var ia, ib [][]*timepoint
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		var old bool
		for second := range chan1 {
			point := check(second)
			ok := point != nil
			if ok {
				if na == nil {
					na = &timepoint{
						second: second,
						point:  point,
					}
				}
				if getOne {
					return
				}
				if old {
					ia[len(ia)-1][1] = &timepoint{
						second: second,
						point:  point,
					}

				} else {
					tp := &timepoint{
						second: second,
						point:  point,
					}
					ia = append(ia, []*timepoint{tp, tp})
				}
			}
			old = ok
		}
	}()
	go func() {
		defer wg.Done()
		var old bool
		for second := range chan2 {
			point := check(second)
			ok := point != nil
			if ok {
				if nb == nil {
					nb = &timepoint{
						second: second,
						point:  point,
					}
				}
				if getOne {
					return
				}
				if old {
					ib[0][0] = &timepoint{
						second: second,
						point:  point,
					}
				} else {
					tp := &timepoint{
						second: second,
						point:  point,
					}
					ib = append([][]*timepoint{{tp, tp}}, ib...)
				}
			}
			old = ok
		}
	}()
	wg.Wait()
	if getOne {
		parts = append(parts, []*timepoint{na, nb})
	} else {
		parts = append(ia, ib...)
	}
	if na == nil && nb != nil {
		na = nb
	} else if na != nil && nb == nil {
		nb = na
	}
	if verbosive {
		if getOne {
			log.Println("range:", na, nb)
		} else {
			log.Println("found", len(parts), "parts:", parts)
		}
	}
	return
}

// Find template image at specified second of the video. Image position is
// returned if image exists, otherwise nil.
func check(second float64) *imagePoint {
	ffmpeg := exec.Command("ffmpeg",
		"-ss", fmt.Sprintf("%.2f", second), "-i", inputFile,
		"-frames:v", "1", "-f", "image2", "pipe:1")
	jpeg, err := ffmpeg.Output()
	if verbosive {
		if err != nil {
			log.Println(ffmpeg, "failed", err)
		} else {
			log.Println("command", ffmpeg, "success")
		}
	}
	if err != nil {
		return nil
	}
	return getLocation(second, jpeg)
}

func getLocation(second float64, file []byte) (loc *imagePoint) {
	src, err := gocv.IMDecode(file, gocv.IMReadGrayScale)
	if err != nil || src.Empty() {
		return nil
	}
	defer src.Close()
	result := gocv.NewMat()
	defer result.Close()
	m := gocv.NewMat()
	defer m.Close()
	for _, template := range templates {
		gocv.MatchTemplate(src, template, &result, gocv.TmCcoeffNormed, m)
		_, maxVal, _, maxLoc := gocv.MinMaxLoc(result)
		if maxVal > 0.9 {
			if verbosive {
				log.Println("found template at:", second, "("+secToTime(int64(second))+")",
					"position:", maxLoc, "score:", maxVal)
			}
			return &imagePoint{
				Width:  template.Cols(),
				Height: template.Rows(),
				Point:  &maxLoc,
			}
		}
	}
	return nil
}

func runCommand(cmd []string) {
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	if dryRun {
		cmdStr := argvToString(cmd)
		fmt.Println(cmdStr)
		if verbosive {
			log.Println("command", cmdStr)
		}
		return
	}
	if verbosive {
		log.Println("running", c)
	}
	err := c.Run()
	if err != nil {
		log.Fatal(err)
	}
}

func generateSeries(from, to, step float64) (series []float64) {
	for i := 0.0; ; i++ {
		v := from + i*step
		if v > to {
			break
		}
		series = append(series, v)
	}
	return
}

func secToTime(sec int64) string {
	return time.Unix(int64(sec), 0).UTC().Format("15:04:05")
}

func argvToString(argv []string) string {
	const specialChars = "`" + `~#$&*()\|[]{};'"<>?! `
	var out []string
	for _, a := range argv {
		if strings.ContainsAny(a, specialChars) {
			out = append(out, strconv.Quote(a))
			continue
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}
