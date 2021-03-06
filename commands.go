package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	humanize "github.com/dustin/go-humanize"
	"github.com/urfave/cli"
)

func user(c *cli.Context) {
	if c.Bool("l") {
		login()
	} else if c.Bool("L") {
		if err := os.Remove(loginInfoFile); err != nil {
			panic(err)
		}
	} else {
		fmt.Println("You are now logged in as", colored(loadLoginInfo().Username, yellow, 1))
	}
}

func printPdf(file string, info problemInfo) {
	pdf, err := exec.Command("pdftotext", file, "-").Output()
	if err != nil {
		panic(err)
	}
	description := string(pdf)
	title := fmt.Sprintf("%d - %s", info.ID, info.Title)
	padding := strings.Repeat(" ", (108-len(title))/2)
	cprintf(white, 1, "%s%s\n\n", padding, title)

	const indent = "       "
	cprintf(white, 1, "Statistics\n")
	fmt.Printf(indent+"* Rate: %.1f %%\n", info.Percentage)
	accepted := humanize.Bytes(uint64(float32(info.TotalSubmissions) * info.Percentage / 100))
	fmt.Printf(indent+"* Total Accepted: %s\n", accepted[:len(accepted)-1])
	submissions := humanize.Bytes(uint64(info.TotalSubmissions))
	fmt.Printf(indent+"* Total Submissions: %s\n\n", submissions[:len(submissions)-1])

	cprintf(white, 1, "Description\n")
	// indentation
	description = strings.Replace(description, "\n", "\n"+indent, -1)
	for _, s := range []string{"Input", "Output", "Sample Input", "Sample Output"} {
		description = strings.Replace(description, indent+s, colored(s, white, 1), 1)
	}
	description = indent + strings.TrimSpace(description)
	fmt.Println(description)
}

func show(c *cli.Context) {
	if c.NArg() == 0 {
		panic("problem id required")
	}
	pid, err := strconv.Atoi(c.Args().First())
	if err != nil {
		panic(err)
	}
	info := getProblemInfo(pid)
	pdfFile := pdfPath + info.getFilename("pdf")
	if !exists(pdfFile) {
		download(fmt.Sprintf("%s/external/%d/p%d.pdf", baseURL, pid/100, pid), pdfFile, "Downloading "+info.Title)
	}

	if c.Bool("g") {
		if err := exec.Command("evince", pdfFile).Run(); err != nil {
			panic(err)
		}
	} else {
		printPdf(pdfFile, info)
	}
}

func touch(c *cli.Context) {
	if c.NArg() == 0 {
		panic("problem ID required")
	}
	pid, err := strconv.Atoi(c.Args().First())
	if err != nil {
		panic(err)
	}
	name := getProblemInfo(pid).getFilename(c.String("lang"))
	f, err := os.Create(name)
	if err != nil {
		panic(err)
	}
	f.Close()
	fmt.Printf("Source code: %s\n", colored(name, yellow, underline))
}

func submit(problemID int, file string, lang int) string {
	category := problemID / 100
	info := getProblemInfo(problemID)
	problemID = info.TrueID
	form := url.Values{
		"problemid": {strconv.Itoa(problemID)},
		"category":  {strconv.Itoa(category)},
		"language":  {strconv.Itoa(lang)},
	}
	f, err := os.Open(file)
	if err != nil {
		panic(err)
	}
	code, err := ioutil.ReadAll(f)
	if err != nil {
		panic(err)
	}
	form.Set("code", string(code))

	// Prevent HTTP 301 redirect
	http.DefaultClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() { http.DefaultClient.CheckRedirect = nil }()
	defer spin("Sending code to judge")()
	resp, err := http.PostForm(baseURL+
		"/index.php?option=com_onlinejudge&Itemid=8&page=save_submission", form)
	if err != nil {
		panic(err)
	}
	resp.Body.Close()
	location := resp.Header["Location"][0]
	sidRegex, _ := regexp.Compile(`Submission\+received\+with\+ID\+(\d+)`)
	submitID := string(sidRegex.FindSubmatch([]byte(location))[1])
	return submitID
}

func getResult(submitID string) (result, runTime string) {
	resp, err := http.Get(baseURL + "/index.php?option=com_onlinejudge&Itemid=9")
	if err != nil {
		panic(err)
	}
	doc, err := goquery.NewDocumentFromResponse(resp)
	if err != nil {
		panic(err)
	}
	row := doc.Find("#col3_content_wrapper > table:nth-child(3) > tbody > tr:nth-child(2) > td")
	if row.First().Text() != submitID {
		panic("not latest submit")
	}
	return strings.TrimSpace(row.Eq(3).Text()), row.Eq(5).Text()
}

func submitAndShowResult(c *cli.Context) {
	if c.NArg() == 0 {
		panic("filename required")
	}
	file := c.Args().First()
	pid, _, ext := parseFilename(file)
	var lang int
	switch ext {
	case "c":
		lang = ansic
	case "java":
		lang = java
	case "cc", "cpp":
		lang = cpp
	case "pas":
		lang = pascal
	case "py":
		lang = python3
	}
	sid := submit(pid, file, lang)
	stop := spin("Waiting for judge result")
	const judging = "In judge queue"
	result := judging
	var runTime string
	for result == judging {
		result, runTime = getResult(sid)
		time.Sleep(1 * time.Second)
	}
	stop()

	if result == "Accepted" {
		cprintf(cyan, bold, "%s Accepted (%ss)\n", yes, runTime)
	} else {
		cprintf(red, bold, "%s %s\n", no, result)
	}
}

func testProgram(c *cli.Context) {
	if c.NArg() == 0 {
		panic("filename required")
	}
	file := c.Args().First()
	pid, _, ext := parseFilename(file)

	// compile source code
	compile, run := getTestCmd(ext, file)
	var cmd *exec.Cmd
	var stop func()
	// for script languages
	if len(compile) > 0 {
		cmd = exec.Command(compile[0], compile[1:]...)
		stop = spin("Compiling")
		out, err := cmd.CombinedOutput()
		stop()
		if err != nil {
			panic(err)
		}
		if len(out) != 0 {
			cprintf(magenta, bold, no+" Warnings\n\n")
			fmt.Print(string(out))
		}
	}

	// get test case from udebug.com
	input, output := getTestData(pid)
	// run the program with test case
	cmd = exec.Command(run[0], run[1:]...)
	tmpfile, err := ioutil.TempFile("", "uva-*.txt")
	if err != nil {
		panic(err)
	}
	defer os.Remove(tmpfile.Name())
	cmd.Stdout = tmpfile
	if input == "" {
		input = "<No input>"
	} else {
		cmd.Stdin = strings.NewReader(input)
	}
	if c.Bool("i") {
		cprintf(green, 0, "Input data:\n")
		fmt.Println(input)
	}
	stop = spin("Running tests")
	start := time.Now()
	if err = cmd.Run(); err != nil {
		panic(err)
	}
	runTime := time.Since(start)
	stop()

	// compare the output with the answer
	cmd = exec.Command("diff", "-Z", "--color=always", tmpfile.Name(), "-")
	cmd.Stdin = strings.NewReader(output)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	err = cmd.Run()
	if err != nil {
		// allow non-zero exit code
		if v, ok := err.(*exec.ExitError); !ok {
			panic(v)
		}
	}
	diff := string(buf.Bytes())
	if len(diff) != 0 {
		cprintf(red, bold, no+" Wrong answer\n")
		fmt.Print(diff)
	} else {
		cprintf(cyan, bold, yes+" Accepted (%.3fs)\n", float32(runTime)/float32(time.Second))
	}
}
