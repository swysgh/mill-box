//go:build darwin || linux || windows

package libbox

import (
	"bytes"
	"time"

	"github.com/swysgh/mill-box/daemon"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/service/powerreport"
)

type powerReportMetadata struct {
	reportMetadata
	StartedAt string `json:"startedAt"`
}

func PowerReportOptions(startedService *daemon.StartedService) powerreport.Options {
	metadata := powerReportMetadata{
		reportMetadata: baseReportMetadata(),
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	return powerreport.Options{
		BasePath:      sWorkingPath,
		Logger:        log.StdLogger(),
		Metadata:      metadata,
		OwnerCallback: chownReport,
		LogCallback: func() []byte {
			return formatLogEntries(startedService.SavedLog())
		},
	}
}

func formatLogEntries(entries []*log.Entry) []byte {
	if len(entries) == 0 {
		return nil
	}
	var buffer bytes.Buffer
	for _, entry := range entries {
		writeWithoutColors(&buffer, entry.Message)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

func writeWithoutColors(buffer *bytes.Buffer, message string) {
	start := 0
	for index := 0; index < len(message); {
		if message[index] != '\x1b' || index+1 >= len(message) || message[index+1] != '[' {
			index++
			continue
		}
		end := index + 2
		for end < len(message) && message[end] != 'm' {
			end++
		}
		if end >= len(message) {
			break
		}
		buffer.WriteString(message[start:index])
		index = end + 1
		start = index
	}
	buffer.WriteString(message[start:])
}

func DiscardPowerReportDraft() {
	powerreport.DiscardDraft(sWorkingPath)
}
