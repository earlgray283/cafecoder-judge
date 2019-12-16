package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
    "io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"pack.ag/tftp"
)

const (
	//BackendHostPort ... appear IP-address and port-number
	BackendHostPort = "localhost:5963"
)

type requestJSON struct {
	SessionID string `json:"sessionID"`
	Command   string `json:"command"`
	Mode      string `json:"mode"` //Mode ... "judge" or "others"
	//Lang      string `json:"lang"` //Lang ... c11,c++17,java8,python3,c#,ruby
}

type cmdResultJSON struct {
	SessionID  string `json:"sessionID"`
	Time       int64  `json:"time"`
	Result     bool   `json:"result"`
	ErrMessage string `json:"errMessage"`
}

type submitT struct {
	sessionID       string //csv[1]
	usercodePath    string
	lang            int
	testcaseDirPath string
	score           int

	execDirPath    string
	execFilePath   string
	testcaseN      int
	testcaseTime   [100]int64
	testcaseResult [100]int

	overallTime   int64
	overallResult int

	containerCli     *client.Client
	containerID      string
	containerInspect types.ContainerJSON

	resultBuffer *bytes.Buffer
	errorBuffer  *bytes.Buffer
}

func checkRegexp(reg, str string) bool {
	return regexp.MustCompile(reg).Match([]byte(str))
}

func fmtWriter(buf *bytes.Buffer, format string, values ...interface{}) {
	arg := fmt.Sprintf(format, values...)
	fmt.Printf(format, values...)
	(*buf).WriteString(arg)
}

func passResultTCP(submit submitT, hostAndPort string) {
	conn, err := net.Dial("tcp", hostAndPort)
	if err != nil {
		fmt.Println(err)
		return
	}
    passStr := strings.Trim(submit.resultBuffer.String(),"\n")+"\n"
    errStr := strings.Trim("error," + submit.sessionID + "," + submit.errorBuffer.String(),"\n")+"\n"
	fmt.Println(passStr)
	fmt.Println(errStr)
	conn.Write([]byte(passStr+errStr))
	conn.Close()
}

func containerStopAndRemove(submit submitT) {
	var err error
	//timeout := 5 * time.Second
	err = submit.containerCli.ContainerStop(context.Background(), submit.containerID, nil)
	if err != nil {
		fmtWriter(submit.errorBuffer, "4:%s\n", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, errC := submit.containerCli.ContainerWait(ctx, submit.containerID, "")
	if err := <-errC; err != nil {
		fmt.Println(err)
	}
	err = submit.containerCli.ContainerRemove(context.Background(), submit.containerID, types.ContainerRemoveOptions{RemoveVolumes: true, RemoveLinks: true, Force: true})
	if err != nil {
		fmtWriter(submit.errorBuffer, "5:%s\n", err)
	}

}

func manageCommands(sessionIDChan *chan string, resultChan *chan int64, errMessageChan *chan string) {
	var cmdResult cmdResultJSON
	listen, err := net.Listen("tcp", "0.0.0.0:3344")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
	}
	for {
		cnct, err := listen.Accept()
		if err != nil {
			continue //continue to receive request
		}
		json.NewDecoder(cnct).Decode(&cmdResult)
		cnct.Close()
		println("connection closed")
		fmt.Println(cmdResult)
		go func() { *(sessionIDChan) <- cmdResult.SessionID }()
		go func() { *(resultChan) <- cmdResult.Time }()
		go func() { *(errMessageChan) <- cmdResult.ErrMessage }()
	}
}

func compile(submit *submitT, sessionIDChan *chan string, resultChan *chan int64, errMessageChan *chan string) int {
	var (
		err      error
		requests requestJSON
	)
	containerConn, err := net.Dial("tcp", submit.containerInspect.NetworkSettings.IPAddress+":8887")
	if err != nil {
		fmtWriter(submit.errorBuffer, "%s\n", err)
		return -2
	}

	requests.SessionID = submit.sessionID
	submit.execDirPath = "/cafecoderUsers/" + submit.sessionID
	switch submit.lang {
	case 0: //C11
		requests.Command = "gcc" + " /cafecoderUsers/" + submit.sessionID + "/Main.c" + " -lm" + " -std=gnu11" + " -o" + " /cafecoderUsers/" + submit.sessionID + "/Main.out"
		submit.execFilePath = "/cafecoderUsers/" + submit.sessionID + "/Main.out"
	case 1: //C++17
		requests.Command = "g++" + " /cafecoderUsers/" + submit.sessionID + "/Main.cpp" + " -lm" + " -std=gnu++17" + " -o" + " /cafecoderUsers/" + submit.sessionID + "/Main.out"
		submit.execFilePath = "/cafecoderUsers/" + submit.sessionID + "/Main.out"
	case 2: //java8
		requests.Command = "javac" + " /cafecoderUsers/" + submit.sessionID + "/Main.java" + " -d" + " /cafecoderUsers/" + submit.sessionID
		submit.execFilePath = "/cafecoderUsers/" + submit.sessionID + "/Main.class"
	case 3: //python3
		requests.Command = "python3" + " -m" + " py_compile" + " /cafecoderUsers/" + submit.sessionID + "/Main.py"
		submit.execFilePath = "/cafecoderUsers/" + submit.sessionID + "/Main.py"
	case 4: //C#
		requests.Command = "mcs" + " /cafecoderUsers/" + submit.sessionID + "/Main.cs" + " -out:/cafecoderUsers/" + submit.sessionID + "/Main.exe"
		submit.execFilePath = "/cafecoderUsers/" + submit.sessionID + "/Main.exe"
	case 5: //Ruby
		requests.Command = "ruby" + " -cw" + " /cafecoderUsers/" + submit.sessionID + "/Main.rb"
		submit.execFilePath = "/cafecoderUsers/" + submit.sessionID + "/Main.rb"
	}

	//I couldn't solve a problem in syntax-chack python3 code.
	//Please teach me how to solve this problem:(
	if submit.lang != 3 && submit.lang != 5 {
    fmt.Println("go compile")
		b, _ := json.Marshal(requests)
		containerConn.Write(b)
		if err != nil {
			fmtWriter(submit.errorBuffer, "%s", err)
			return -2
		}
		containerConn.Close()
        fmt.Println("wait for compile...")
		for {
			if submit.sessionID == <-*sessionIDChan {
                fmtWriter(submit.errorBuffer, "%s\n", <-*errMessageChan)
				break
			}
		}
    fmt.Println("compile done")
	}

	containerConn, err = net.Dial("tcp", submit.containerInspect.NetworkSettings.IPAddress+":8887")
    defer containerConn.Close()
	if err != nil {
		fmtWriter(submit.errorBuffer, "%s\n", err)
		return -2
	}
	requests.Command = "chown rbash_user " + submit.execFilePath
	b, _ := json.Marshal(requests)
	containerConn.Write(b)
	containerConn.Close()
	for {
		if submit.sessionID == <-*sessionIDChan {
            fmtWriter(submit.errorBuffer, "%s\n", <-*errMessageChan)
			break
		}
	}

	return 0
}

func tryTestcase(submit *submitT, sessionIDChan *chan string, resultChan *chan int64, errMessageChan *chan string) int {
	var (
		//stderr     bytes.Buffer
		requests     requestJSON
		testcaseName [256]string
	)
	requests.SessionID = submit.sessionID

	testcaseListFile, err := os.Open(submit.testcaseDirPath + "/testcase_list.txt")
	if err != nil {
		fmtWriter(submit.errorBuffer, "failed to open"+submit.testcaseDirPath+"/testcase_list.txt\n")
		return -1
	}

	scanner := bufio.NewScanner(testcaseListFile)
	for scanner.Scan() {
		testcaseName[submit.testcaseN] = scanner.Text()
		submit.testcaseN++
	}
	testcaseListFile.Close()

	for i := 0; i < submit.testcaseN; i++ {
		testcaseName[i] = strings.TrimSpace(testcaseName[i]) //delete \n\r
		outputTestcase, err := ioutil.ReadFile(submit.testcaseDirPath + "/out/" + testcaseName[i])
		if err != nil {
			fmtWriter(submit.errorBuffer, "%s\n", err)
			return -1
		}
		testcaseFile, _ := os.Open(submit.testcaseDirPath + "/in/" + testcaseName[i])
		submit.containerCli.CopyToContainer(context.Background(), submit.sessionID, "/cafecoderUsers/"+submit.sessionID+"/testcase.txt", bufio.NewReader(testcaseFile), types.CopyToContainerOptions{})
		testcaseFile.Close()

		containerConn, err := net.Dial("tcp", submit.containerInspect.NetworkSettings.IPAddress+":8887")
		if err != nil {
			fmtWriter(submit.errorBuffer, "%s\n", err)
			return -1
		}
		switch submit.lang {
		case 0: //C11
			requests.Command = "timeout 3 ./cafecoderUsers/" + submit.sessionID + "/Main.out"
		case 2: //java8
			requests.Command = "timeout 3 java -cp /cafecoderUsers/" + submit.sessionID + "/Main"
		case 3: //python3
			requests.Command = "timeout 3 python3 /cafecoderUsers/" + submit.sessionID + "/Main.py"
		case 4: //C#
			requests.Command = "timeout 3 mono /cafecoderUsers/" + submit.sessionID + "/Main.out"
		case 5: //Ruby
			requests.Command = "timeout 3 ./cafecoderUsers/" + submit.sessionID + "/Main.out"
		}
		requests.Mode = "judge"
		b, _ := json.Marshal(requests)
		containerConn.Write(b)
		containerConn.Close()
        fmt.Println("wait for testcase...")
		for {
			if submit.sessionID == <-(*sessionIDChan) {
				break
			}
		}

		userStdoutReader, _, err := submit.containerCli.CopyFromContainer(context.TODO(), submit.sessionID, "cafecoderUsers/"+submit.sessionID+"/userStdout.txt")
		if err != nil {
			fmtWriter(submit.errorBuffer, "1:%s\n", err)
			return -1
		}
		userStdout := new(bytes.Buffer)
		userStdout.ReadFrom(userStdoutReader)

		userStderrReader, _, err := submit.containerCli.CopyFromContainer(context.TODO(), submit.sessionID, "cafecoderUsers/"+submit.sessionID+"/userStderr.txt")
		if err != nil {
			fmtWriter(submit.errorBuffer, "2:%s\n", err)
			return -1
		}
		userStderr := new(bytes.Buffer)
		userStdout.ReadFrom(userStderrReader)

        fmt.Println("time")
		submit.testcaseTime[i] = <-*resultChan
        fmt.Println(submit.testcaseTime[i])
		if submit.overallTime < submit.testcaseTime[i] {
			submit.overallTime = submit.testcaseTime[i]
		}

		userStdoutLines := strings.Split(userStdout.String(), "\n")
		userStderrLines := strings.Split(userStderr.String(), "\n")
		outputTestcaseLines := strings.Split(string(outputTestcase), "\n")

		if submit.testcaseTime[i] <= 2000 {
			if userStderr.String() != "" {
				for j := 0; j < len(userStderrLines); j++ {
					fmtWriter(submit.errorBuffer, "%s\n", userStderrLines[j])
				}
				submit.testcaseResult[i] = 3 //RE
			} else {
				submit.testcaseResult[i] = 1 //WA
				for j := 0; j < len(userStdoutLines) && j < len(outputTestcaseLines); j++ {
					submit.testcaseResult[i] = 0 //AC
					if strings.TrimSpace(string(userStdoutLines[j])) != strings.TrimSpace(string(outputTestcaseLines[j])) {
						submit.testcaseResult[i] = 1 //WA
						break
					}
				}
			}
		} else {
			submit.testcaseResult[i] = 2 //TLE
		}
		if submit.testcaseResult[i] > submit.overallResult {
			submit.overallResult = submit.testcaseResult[i]
		}
	}
	return 0
}

func fileCopy(dstName string, srcName string) {
    src, err := os.Open(srcName)
    if err != nil {
        panic(err)
    }
    defer src.Close()

    dst, err := os.Create(dstName)
    if err != nil {
        panic(err)
    }
    defer dst.Close()

    _, err = io.Copy(dst, src)
    if  err != nil {
        panic(err)
    }
}
func executeJudge(csv []string, tftpCli *tftp.Client, sessionIDChan chan string, resultChan chan int64, errMessageChan chan string) {
	var (
		result        = []string{"AC", "WA", "TLE", "RE", "MLE", "CE", "IE"}
		langExtention = [...]string{".c", ".cpp", ".java", ".py", ".cs", ".rb"}
		submit        = submitT{errorBuffer: new(bytes.Buffer), resultBuffer: new(bytes.Buffer)}
		err           error
	)

	/*validation checks*/
	for i := range csv {
		if !checkRegexp(`[(A-Za-z0-9\./_\/)]*`, strings.TrimSpace(csv[i])) {
			fmtWriter(submit.resultBuffer, "%s,-1,undef,%s,0,", submit.sessionID, result[6])
			fmtWriter(submit.errorBuffer, "Inputs are included another characters[0-9],[a-z],[A-Z],'.','/','_'\n")
			passResultTCP(submit, BackendHostPort)
			return
		}
	}

	if len(csv) > 1 {
		submit.sessionID = csv[1]
	}
	if len(csv) > 6 {
		fmtWriter(submit.resultBuffer, "%s,-1,undef,%s,0,", submit.sessionID, result[6])
		fmtWriter(submit.errorBuffer, "too many args\n")
		passResultTCP(submit, BackendHostPort)
		return
	}
	if len(csv) < 6 {
		fmtWriter(submit.resultBuffer, "%s,-1,undef,%s,0,", submit.sessionID, result[6])
		fmtWriter(submit.errorBuffer, "too few args\n")
		passResultTCP(submit, BackendHostPort)
		return
	}

	submit.usercodePath = csv[2]
	submit.lang, _ = strconv.Atoi(csv[3])
	submit.testcaseDirPath = csv[4]
	submit.score, _ = strconv.Atoi(csv[5])

	os.Mkdir("cafecoderUsers/"+submit.sessionID, 0777)
	fileCopy("cafecoderUsers/"+submit.sessionID+"/"+submit.sessionID, submit.usercodePath)
	defer os.Remove("cafecoderUsers/" + submit.sessionID)

	//download file
	//submit.code = tftpwrapper.DownloadFromPath(&tftpCli, submit.usercodePath)

	/*--------------------------------about docker--------------------------------*/
	submit.containerCli, err = client.NewClientWithOpts(client.WithVersion("1.35"))
	if err != nil {
		fmtWriter(submit.errorBuffer, "%s\n", err)
		passResultTCP(submit, BackendHostPort)
        return
	}
	config := &container.Config{

		Image: "cafecoder",
	}
	resp, err := submit.containerCli.ContainerCreate(context.TODO(), config, nil, nil, strings.TrimSpace(submit.sessionID))
	if err != nil {
		fmtWriter(submit.errorBuffer, "2:%s\n", err)
		passResultTCP(submit, BackendHostPort)
        return
	}
	submit.containerID = resp.ID
	err = submit.containerCli.ContainerStart(context.TODO(), submit.containerID, types.ContainerStartOptions{})
	if err != nil {
		fmtWriter(submit.errorBuffer, "3:%s\n", err)
		passResultTCP(submit, BackendHostPort)
        return
	}

	defer containerStopAndRemove(submit)

	//get container IP address
	submit.containerInspect, _ = submit.containerCli.ContainerInspect(context.TODO(), submit.containerID)
	/*----------------------------------------------------------------------------*/

	containerConn, err := net.Dial("tcp", submit.containerInspect.NetworkSettings.IPAddress+":8887")
	if err != nil {
		fmtWriter(submit.errorBuffer, "%s\n", err)
		passResultTCP(submit, BackendHostPort)
		return
	}

	var requests requestJSON
	requests.Command = "mkdir -p cafecoderUsers/" + submit.sessionID
	requests.SessionID = submit.sessionID
	b, err := json.Marshal(requests)
	if err != nil {
		fmtWriter(submit.errorBuffer, "%s\n", err)
		passResultTCP(submit, BackendHostPort)
        return
	}
	println(string(b))
	containerConn.Write(b)
	containerConn.Close()
	for {
		if submit.sessionID == <-sessionIDChan {
            fmtWriter(submit.errorBuffer, "%s\n", <-errMessageChan)
            break
		}
	}
	println("check")

	usercodeFile, _ := os.Open("cafecoderUsers/" + submit.sessionID + "/" + submit.sessionID)
	submit.containerCli.CopyToContainer(
		context.TODO(), submit.containerID,
		"cafecoderUsers/"+submit.sessionID+"/Main"+langExtention[submit.lang],
		usercodeFile, types.CopyToContainerOptions{},
	)
	usercodeFile.Close()

	ret := compile(&submit, &sessionIDChan, &resultChan, &errMessageChan)
	if ret == -1 {
		fmtWriter(submit.resultBuffer, "%s,-1,undef,%s,0,", submit.sessionID, result[6])
		passResultTCP(submit, BackendHostPort)
		return
	} else if ret == -2 {
		fmtWriter(submit.resultBuffer, "%s,-1,undef,%s,0,", submit.sessionID, result[5])
		passResultTCP(submit, BackendHostPort)
		return
	}

	ret = tryTestcase(&submit, &sessionIDChan, &resultChan, &errMessageChan)
	if ret == -1 {
		fmtWriter(submit.resultBuffer, "%s,-1,undef,%s,0,", submit.sessionID, result[6])
		passResultTCP(submit, BackendHostPort)
		return
	}
	fmtWriter(submit.resultBuffer, "%s,%d,undef,%s,", submit.sessionID, submit.overallTime, result[submit.overallResult])
	if submit.overallResult == 0 {
		fmtWriter(submit.resultBuffer, "%d,", submit.score)
	} else {
		fmtWriter(submit.resultBuffer, "0,")
	}
	for i := 0; i < submit.testcaseN; i++ {
		fmtWriter(submit.resultBuffer, "%s,%d,", result[submit.testcaseResult[i]], submit.testcaseTime[i])
	}
    passResultTCP(submit, BackendHostPort)
}

func main() {

	sessionIDChan := make(chan string)
	resultChan := make(chan int64)
	errMessageChan := make(chan string)
	go manageCommands(&sessionIDChan, &resultChan, &errMessageChan)
	listen, err := net.Listen("tcp", "0.0.0.0:8888")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
	}
	tftpCli, err := tftp.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
	}
	for {
		cnct, err := listen.Accept()
		if err != nil {
			continue //continue to receive request
		}
		message, err := bufio.NewReader(cnct).ReadString('\n')
		println(string(message))
		//reader := csv.NewReader(messageLen)
		cnct.Close()
		println("connection closed")
		go executeJudge(strings.Split(message, ","), tftpCli, sessionIDChan, resultChan, errMessageChan)
	}
}
