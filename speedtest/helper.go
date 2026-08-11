package speedtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/gocarina/gocsv"
	"github.com/librespeed/speedtest-cli/defs"
	"github.com/librespeed/speedtest-cli/output"
	"github.com/librespeed/speedtest-cli/report"
	"github.com/urfave/cli/v2"
)

const (
	// the default ping count for measuring ping and jitter
	pingCount = 10
)

// doSpeedTest is where the actual speed test happens
func doSpeedTest(c *cli.Context, servers []defs.Server, telemetryServer defs.TelemetryServer, network string, silent bool, noICMP bool) error {
	if serverCount := len(servers); serverCount > 1 {
		output.WriteUI("Testing against %d servers\n", serverCount)
	}

	var reps_json []report.JSONReport
	var reps_csv []report.CSVReport

	// fetch current user's IP info
	for _, currentServer := range servers {
		// get telemetry level
		currentServer.TLog.SetLevel(telemetryServer.GetLevel())

		u, err := currentServer.GetURL()
		if err != nil {
			output.WriteError("Failed to get server URL: %s\n", err)
			return err
		}

		output.WriteUI("Selected server: %s [%s]\n", output.Sanitize(currentServer.Name), output.Sanitize(u.Hostname()))

		if sponsorMsg := currentServer.Sponsor(); sponsorMsg != "" {
			output.WriteUI("Sponsored by: %s\n", output.Sanitize(sponsorMsg))
		}

		if currentServer.IsUp() {
			ispInfo, err := currentServer.GetIPInfo(c.String(defs.OptionDistance))
			if err != nil {
				output.WriteError("Failed to get IP info: %s\n", err)
				return err
			}
			output.WriteUI("You're testing from: %s\n", output.Sanitize(ispInfo.ProcessedString))

			// get ping and jitter value
			var pb *spinner.Spinner
			if !silent {
				pb = spinner.New(spinner.CharSets[11], 100*time.Millisecond, spinner.WithWriterFile(os.Stderr))
				pb.Prefix = "Pinging server...  "
				pb.Start()
			}

			// skip ICMP if option given
			currentServer.NoICMP = noICMP

			p, jitter, err := currentServer.ICMPPingAndJitter(pingCount, c.String(defs.OptionSource), network)
			if err != nil {
				output.WriteError("Failed to get ping and jitter: %s\n", err)
				return err
			}

			if pb != nil {
				// print the result ourselves instead of via pb.FinalMSG: the
				// spinner only prints it when it was actually running, which it
				// isn't when stderr is not a terminal
				pb.Stop()
				output.WriteUI("Ping: %.2f ms\tJitter: %.2f ms\n", p, jitter)
			}

			// get download value
			var downloadValue float64
			var bytesRead uint64
			if c.Bool(defs.OptionNoDownload) {
				output.WriteUI("Download test is disabled\n")
			} else {
				download, br, err := currentServer.Download(silent, c.Bool(defs.OptionBytes), c.Bool(defs.OptionMebiBytes), c.Int(defs.OptionConcurrent), c.Int(defs.OptionChunks), time.Duration(c.Int(defs.OptionDuration))*time.Second)
				if err != nil {
					output.WriteError("Failed to get download speed: %s\n", err)
					return err
				}
				downloadValue = download
				bytesRead = br
			}

			// get upload value
			var uploadValue float64
			var bytesWritten uint64
			if c.Bool(defs.OptionNoUpload) {
				output.WriteUI("Upload test is disabled\n")
			} else {
				upload, bw, err := currentServer.Upload(c.Bool(defs.OptionNoPreAllocate), silent, c.Bool(defs.OptionBytes), c.Bool(defs.OptionMebiBytes), c.Int(defs.OptionConcurrent), c.Int(defs.OptionUploadSize), time.Duration(c.Int(defs.OptionDuration))*time.Second)
				if err != nil {
					output.WriteError("Failed to get upload speed: %s\n", err)
					return err
				}
				uploadValue = upload
				bytesWritten = bw
			}

			// print result if --simple is given
			if c.Bool(defs.OptionSimple) {
				if c.Bool(defs.OptionBytes) {
					useMebi := c.Bool(defs.OptionMebiBytes)
					output.WriteOut("Ping:\t%.2f ms\tJitter:\t%.2f ms\nDownload rate:\t%s\nUpload rate:\t%s\n", p, jitter, humanizeMbps(downloadValue, useMebi), humanizeMbps(uploadValue, useMebi))
				} else {
					output.WriteOut("Ping:\t%.2f ms\tJitter:\t%.2f ms\nDownload rate:\t%.2f Mbps\nUpload rate:\t%.2f Mbps\n", p, jitter, downloadValue, uploadValue)
				}
			}

			// print share link if --share is given
			var shareLink string
			if telemetryServer.GetLevel() > 0 {
				var extra defs.TelemetryExtra
				extra.ServerName = currentServer.Name
				extra.Extra = c.String(defs.OptionTelemetryExtra)

				if link, err := sendTelemetry(telemetryServer, ispInfo, downloadValue, uploadValue, p, jitter, currentServer.TLog.String(), extra); err != nil {
					output.WriteError("Error when sending telemetry data: %s\n", err)
				} else {
					shareLink = link
					// only print to stdout when --json and --csv are not used
					if !c.Bool(defs.OptionJSON) && !c.Bool(defs.OptionCSV) {
						if c.Bool(defs.OptionSimple) {
							output.WriteOut("Share your result: %s\n", link)
						} else {
							output.WriteUI("Share your result: %s\n", link)
						}
					}
				}
			}

			// check for --csv or --json. the program prioritize the --csv before the --json. this is the same behavior as speedtest-cli
			if c.Bool(defs.OptionCSV) {
				// print csv if --csv is given
				var rep report.CSVReport
				rep.Timestamp = time.Now()

				rep.Name = currentServer.Name
				rep.Address = u.String()
				rep.Ping = math.Round(p*100) / 100
				rep.Jitter = math.Round(jitter*100) / 100
				rep.Download = math.Round(downloadValue*100) / 100
				rep.Upload = math.Round(uploadValue*100) / 100
				rep.Share = shareLink
				rep.IP = ispInfo.RawISPInfo.IP

				reps_csv = append(reps_csv, rep)
			} else if c.Bool(defs.OptionJSON) {
				// print json if --json is given
				var rep report.JSONReport
				rep.Timestamp = time.Now()

				rep.Ping = math.Round(p*100) / 100
				rep.Jitter = math.Round(jitter*100) / 100
				rep.Download = math.Round(downloadValue*100) / 100
				rep.Upload = math.Round(uploadValue*100) / 100
				rep.BytesReceived = bytesRead
				rep.BytesSent = bytesWritten
				rep.Share = shareLink

				rep.Server.Name = currentServer.Name
				rep.Server.URL = u.String()

				rep.Client = report.Client{IPInfoResponse: ispInfo.RawISPInfo}
				rep.Client.Readme = ""

				reps_json = append(reps_json, rep)
			}
		} else {
			output.WriteUI("Selected server %s (%s) is not responding at the moment, try again later\n", output.Sanitize(currentServer.Name), output.Sanitize(u.Hostname()))
		}

		//add a new line after each test if testing multiple servers
		if len(servers) > 1 && !silent {
			output.WriteUIBlank()
		}
	}

	// check for --csv or --json. the program prioritize the --csv before the --json. this is the same behavior as speedtest-cli
	if c.Bool(defs.OptionCSV) {
		var buf bytes.Buffer
		if err := gocsv.MarshalWithoutHeaders(&reps_csv, &buf); err != nil {
			output.WriteError("Error generating CSV report: %s\n", err)
		} else {
			os.Stdout.WriteString(strings.TrimRight(buf.String(), "\n\r"))
		}
	} else if c.Bool(defs.OptionJSON) {
		if b, err := json.Marshal(&reps_json); err != nil {
			output.WriteError("Error generating JSON report: %s\n", err)
		} else {
			os.Stdout.Write(b[:])
		}
	}

	return nil
}

// sendTelemetry sends the telemetry result to server, if --share is given
func sendTelemetry(telemetryServer defs.TelemetryServer, ispInfo *defs.GetIPResult, download, upload, pingVal, jitter float64, logs string, extra defs.TelemetryExtra) (string, error) {
	var buf bytes.Buffer
	wr := multipart.NewWriter(&buf)

	b, _ := json.Marshal(ispInfo)
	if fIspInfo, err := wr.CreateFormField("ispinfo"); err != nil {
		output.WriteDebug("Error creating form field: %s\n", err)
		return "", err
	} else if _, err = fIspInfo.Write(b); err != nil {
		output.WriteDebug("Error writing form field: %s\n", err)
		return "", err
	}

	if fDownload, err := wr.CreateFormField("dl"); err != nil {
		output.WriteDebug("Error creating form field: %s\n", err)
		return "", err
	} else if _, err = fDownload.Write([]byte(strconv.FormatFloat(download, 'f', 2, 64))); err != nil {
		output.WriteDebug("Error writing form field: %s\n", err)
		return "", err
	}

	if fUpload, err := wr.CreateFormField("ul"); err != nil {
		output.WriteDebug("Error creating form field: %s\n", err)
		return "", err
	} else if _, err = fUpload.Write([]byte(strconv.FormatFloat(upload, 'f', 2, 64))); err != nil {
		output.WriteDebug("Error writing form field: %s\n", err)
		return "", err
	}

	if fPing, err := wr.CreateFormField("ping"); err != nil {
		output.WriteDebug("Error creating form field: %s\n", err)
		return "", err
	} else if _, err = fPing.Write([]byte(strconv.FormatFloat(pingVal, 'f', 2, 64))); err != nil {
		output.WriteDebug("Error writing form field: %s\n", err)
		return "", err
	}

	if fJitter, err := wr.CreateFormField("jitter"); err != nil {
		output.WriteDebug("Error creating form field: %s\n", err)
		return "", err
	} else if _, err = fJitter.Write([]byte(strconv.FormatFloat(jitter, 'f', 2, 64))); err != nil {
		output.WriteDebug("Error writing form field: %s\n", err)
		return "", err
	}

	if fLog, err := wr.CreateFormField("log"); err != nil {
		output.WriteDebug("Error creating form field: %s\n", err)
		return "", err
	} else if _, err = fLog.Write([]byte(logs)); err != nil {
		output.WriteDebug("Error writing form field: %s\n", err)
		return "", err
	}

	b, _ = json.Marshal(extra)
	if fExtra, err := wr.CreateFormField("extra"); err != nil {
		output.WriteDebug("Error creating form field: %s\n", err)
		return "", err
	} else if _, err = fExtra.Write(b); err != nil {
		output.WriteDebug("Error writing form field: %s\n", err)
		return "", err
	}

	if err := wr.Close(); err != nil {
		output.WriteDebug("Error flushing form field writer: %s\n", err)
		return "", err
	}

	telemetryUrl, err := telemetryServer.GetPath()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, telemetryUrl.String(), &buf)
	if err != nil {
		output.WriteDebug("Error when creating HTTP request: %s\n", err)
		return "", err
	}
	req.Header.Set("Content-Type", wr.FormDataContentType())
	req.Header.Set("User-Agent", defs.UserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		output.WriteDebug("Error when making HTTP request: %s\n", err)
		return "", err
	}
	defer resp.Body.Close()

	id, err := io.ReadAll(resp.Body)
	if err != nil {
		output.WriteError("Error when reading HTTP request: %s\n", err)
		return "", err
	}

	resultUrl, err := telemetryServer.GetShare()
	if err != nil {
		return "", err
	}

	if str := strings.Split(string(id), " "); len(str) != 2 {
		return "", fmt.Errorf("server returned invalid response: %s", id)
	} else {
		q := resultUrl.Query()
		q.Set("id", str[1])
		resultUrl.RawQuery = q.Encode()

		return resultUrl.String(), nil
	}
}

func humanizeMbps(mbps float64, useMebi bool) string {
	val := mbps / 8
	var base float64 = 1000
	if useMebi {
		base = 1024
	}

	if val < 1 {
		if kb := val * base; kb < 1 {
			return fmt.Sprintf("%.2f bytes/s", kb*base)
		} else {
			return fmt.Sprintf("%.2f KB/s", kb)
		}
	} else if val > base {
		return fmt.Sprintf("%.2f GB/s", val/base)
	} else {
		return fmt.Sprintf("%.2f MB/s", val)
	}
}
