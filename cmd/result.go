package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/spikeekips/sebak-hot-body/hotbody"

	"boscoin.io/sebak/lib/common"
)

var (
	resultCmd    *cobra.Command
	resultOutput *os.File
	started      time.Time
	ended        time.Time
)

func init() {
	resultCmd = &cobra.Command{
		Use:   "result <result log>",
		Short: "Parse result",
		Run: func(c *cobra.Command, args []string) {
			parseResultFlags(args)

			runResult()
		},
	}

	resultCmd.Flags().StringVar(&flagLogLevel, "log-level", flagLogLevel, "log level, {crit, error, warn, info, debug}")
	resultCmd.Flags().StringVar(&flagLogFormat, "log-format", flagLogFormat, "log format, {terminal, json}")
	resultCmd.Flags().StringVar(&flagLog, "log", flagLog, "set log file")
	resultCmd.Flags().BoolVar(&flagBrief, "brief", flagBrief, "show only result")

	rootCmd.AddCommand(resultCmd)
}

func parseResultFlags(args []string) {
	var err error

	setLogging()

	if len(args) < 1 {
		printError(resultCmd, fmt.Errorf("<result log> is missing"))
	}
	flagResultOutput = args[0]

	if resultOutput, err = os.Open(flagResultOutput); err != nil {
		printError(resultCmd, fmt.Errorf("failed to open <result log>; %v", err))
	}

	parsedFlags := []interface{}{}
	parsedFlags = append(parsedFlags, "\n\tresult-log", flagResultOutput)
	parsedFlags = append(parsedFlags, "\n\tlog-level", flagLogLevel)
	parsedFlags = append(parsedFlags, "\n\tlog-format", flagLogFormat)
	parsedFlags = append(parsedFlags, "\n\tlog", flagLog)
	parsedFlags = append(parsedFlags, "\n", "")

	log.Debug("parsed flags:", parsedFlags...)
}

func loadLine(l string) (record hotbody.Record, err error) {
	var d map[string]interface{}
	if err = json.Unmarshal([]byte(l), &d); err != nil {
		return
	}

	if _, found := d["type"]; !found {
		err = fmt.Errorf("found invalid format")
		return
	}

	recordType := d["type"].(string)
	switch recordType {
	case "started":
		started, _ = common.ParseISO8601(d["time"].(string))
		return
	case "ended":
		ended, _ = common.ParseISO8601(d["time"].(string))
		return
	case "config":
		var b []byte
		if b, err = json.Marshal(d["config"]); err != nil {
			return
		}
		var hotterConfig hotbody.HotterConfig
		if err = json.Unmarshal(b, &hotterConfig); err != nil {
			return
		}

		record = hotterConfig
	case "create-accounts":
		var createAccounts hotbody.RecordCreateAccounts
		if err = json.Unmarshal([]byte(l), &createAccounts); err != nil {
			return
		}

		record = createAccounts
	case "payment":
		var payment hotbody.RecordPayment
		if err = json.Unmarshal([]byte(l), &payment); err != nil {
			return
		}

		record = payment
	case "sebak-error":
		var sebakError hotbody.RecordSEBAKError
		if err = json.Unmarshal([]byte(l), &sebakError); err != nil {
			return
		}

		record = sebakError
	default:
		err = fmt.Errorf("unknown type found: %v", recordType)
		return
	}

	return
}

func runResult() {
	defer resultOutput.Close()

	var err error

	sc := bufio.NewScanner(resultOutput)
	sc.Split(bufio.ScanLines)

	var config hotbody.HotterConfig

	sc.Scan()
	headLine := sc.Text()

	var record hotbody.Record
	if record, err = loadLine(headLine); err != nil {
		printError(resultCmd, fmt.Errorf("something wrong to read <result log>; %v; %v", err, headLine))
	} else {
		config = record.(hotbody.HotterConfig)
	}
	log.Debug("config loaded", "config", config)

	log.Debug("trying to load record")
	var records []hotbody.Record
	sebakErrors := map[int]int{}
	for sc.Scan() {
		s := sc.Text()

		if record, err = loadLine(s); err != nil {
			printError(resultCmd, fmt.Errorf("something wrong to read <result log>; %v; %v", err, s))
		} else if record == nil {
			continue
		}
		if record.GetType() != "payment" {
			if sr, ok := record.(hotbody.RecordSEBAKError); ok {
				e := sr.GetRawError()
				if _, ok := e["data"]; !ok {
					continue
				}

				j := e["data"].(map[string]interface{})["body"].(string)

				var body map[string]interface{}
				json.Unmarshal([]byte(j), &body)
				if _, ok := body["code"]; !ok {
					continue
				}
				sebakErrors[int(body["code"].(float64))]++
			}

			continue
		}

		records = append(records, record)
	}
	log.Debug("records loaded", "count", len(records))

	if len(records) < 1 {
		fmt.Println("no records found")
		os.Exit(1)
	}

	if err = sc.Err(); err != nil {
		printError(resultCmd, fmt.Errorf("something wrong to read <result log>; %v", err))
	}

	var maxElapsedTime float64
	var minElapsedTime float64 = -1
	var step float64 = 50000000000

	els := map[float64]int{}
	var countError int
	errorTypes := map[hotbody.RecordErrorType]int{}
	for _, r := range records {
		es := float64(r.GetElapsed())

		i := int(es/step) * int(step)
		els[float64(i)]++

		maxElapsedTime = math.Max(maxElapsedTime, es)
		if minElapsedTime < 0 {
			minElapsedTime = es
		} else {
			minElapsedTime = math.Min(minElapsedTime, es)
		}

		if r.GetError() == nil {
			continue
		}
		countError++
		errorTypes[r.GetErrorType()]++
	}

	var elsKeys sort.IntSlice
	for i := float64(0); i < ((maxElapsedTime/step)*step)+step; i += step {
		if _, ok := els[i]; !ok {
			els[i] = 0
		}
		elsKeys = append(elsKeys, int(i))
	}

	sort.Sort(elsKeys)

	alignKey := func(s string) string {
		return fmt.Sprintf("% 20s", s)
	}

	alignValue := func(v interface{}) string {
		var s string
		switch v.(type) {
		case float64:
			s = fmt.Sprintf("%15.10f", v)
		default:
			s = fmt.Sprintf("%v", v)
		}

		return fmt.Sprintf("%30s", s)
	}

	alignHead := func(s string) string {
		return fmt.Sprintf("* %-10s", s)
	}

	formatAddress := func(s string) string {
		return fmt.Sprintf("%s...%s", s[:13], s[len(s)-13:])
	}
	table := tablewriter.NewWriter(os.Stdout)
	var Row = [][]string{}

	if !flagBrief {
		Row = append(Row, []string{alignHead("config"), alignKey("testing time"), alignValue(config.Timeout)})
		Row = append(Row, []string{alignHead("config"), alignKey("concurrent requests"), alignValue(config.T)})
		Row = append(Row, []string{alignHead("config"), alignKey("initial account"), alignValue(formatAddress(config.InitAccount))})
		Row = append(Row, []string{alignHead("config"), alignKey("request timeout"), alignValue(config.RequestTimeout)})
		Row = append(Row, []string{alignHead("config"), alignKey("confirm duration"), alignValue(config.ConfirmDuration)})
		Row = append(Row, []string{alignHead("config"), alignKey("operations"), alignValue(config.Operations)})
		Row = append(Row, []string{alignHead("network"), alignKey("network id"), alignValue(config.Node.Policy.NetworkID)})
		Row = append(Row, []string{alignHead("network"), alignKey("initial balance"), alignValue(config.Node.Policy.InitialBalance)})
		Row = append(Row, []string{alignHead("network"), alignKey("block time"), alignValue(config.Node.Policy.BlockTime)})
		Row = append(Row, []string{alignHead("network"), alignKey("base reserve"), alignValue(config.Node.Policy.BaseReserve)})
		Row = append(Row, []string{alignHead("network"), alignKey("base fee"), alignValue(config.Node.Policy.BaseFee)})
		Row = append(Row, []string{alignHead("node"), alignKey("endpoint"), alignValue(alignValue(config.Node.Node.Endpoint))})
		Row = append(Row, []string{alignHead("node"), alignKey("address"), formatAddress(config.Node.Node.Address)})
		Row = append(Row, []string{alignHead("node"), alignKey("state"), alignValue(config.Node.Node.State)})
		Row = append(Row, []string{alignHead("node"), alignKey("block height"), alignValue(config.Node.Block.Height)})
		Row = append(Row, []string{alignHead("node"), alignKey("block hash"), alignValue(formatAddress(config.Node.Block.Hash))})
		Row = append(Row, []string{alignHead("node"), alignKey("block totaltxs"), alignValue(config.Node.Block.TotalTxs)})
		Row = append(Row, []string{alignHead("node"), alignKey("block totalops"), alignValue(config.Node.Block.TotalOps)})
	}

	lastTime := records[len(records)-1].GetTime()

	if !flagBrief {
		Row = append(Row, []string{alignHead("time"), alignKey("started"), alignValue(FormatISO8601(started))})
		Row = append(Row, []string{alignHead("time"), alignKey("ended"), alignValue(FormatISO8601(lastTime))})
		Row = append(Row, []string{alignHead("time"), alignKey("total elapsed"), alignValue(lastTime.Sub(started))})
	}

	{

		Row = append(Row, []string{alignHead("result"), alignKey("# requests"), alignValue(len(records))})
		Row = append(Row, []string{alignHead("result"), alignKey("# operations"), alignValue(len(records) * config.Operations)})
		Row = append(Row, []string{
			alignHead("result"),
			alignKey("error rates"),
			alignValue(
				fmt.Sprintf(
					"%2.5f％ (%d/%d)",
					float64(countError)/float64(len(records))*100,
					countError,
					len(records),
				),
			),
		})
		Row = append(Row, []string{alignHead("result"), alignKey("max elapsed time"), alignValue(maxElapsedTime / float64(10000000000))})
		Row = append(Row, []string{alignHead("result"), alignKey("min elapsed time"), alignValue(minElapsedTime / float64(10000000000))})
		Row = append(Row, []string{alignHead("result"), alignKey("distribution"), ""})
		for _, e := range elsKeys {
			span := int(float64(e) / float64(10000000000))
			c := els[float64(e)]

			Row = append(Row, []string{
				alignHead("result"),
				"", 
				alignValue(
					fmt.Sprintf(
						"%2d-%-2d: %8.5f％ / %5d",
						span,
						span+int(step/float64(10000000000)),
						float64(c)/float64(len(records))*100,
						c,
					),
				),
			})
		}

		totalSeconds := lastTime.Sub(started).Seconds()

		ops := float64((len(records))*config.Operations) / float64(totalSeconds)
		Row = append(Row, []string{alignHead("result"), alignKey("expected OPS"), string(int(ops))})
		ops = float64((len(records)-countError)*config.Operations) / float64(totalSeconds)
		Row = append(Row, []string{alignHead("result"), alignKey("real OPS"), string(int(ops))})
	}

	{
		if countError < 1 {
			Row = append(Row, []string{alignHead("error"), alignKey("no error"), ""})
		} else {
			var c int
			for errorType, errorCount := range errorTypes {
				h := ""
				if c == 0 {
					h = alignHead("error")
				}
				c++
				Row = append(Row, []string{
					h,
					alignKey(string(errorType)),
					alignValue(
						fmt.Sprintf(
							"%d | % 10s",
							errorCount,
							fmt.Sprintf(
								"%.5f％",
								float64(errorCount)/float64(countError)*100,
							),
						),
					),
				})
			}
		}
	}

	{
		if len(sebakErrors) < 1 {
			Row = append(Row, []string{alignHead("sebak-error"), alignKey("no error"), ""})
		} else {
			var countSEBAKError int
			for _, errorCount := range sebakErrors {
				countSEBAKError += errorCount
			}

			var c int
			for errorType, errorCount := range sebakErrors {
				h := ""
				if c == 0 {
					h = alignHead("sebak-error")
				}
				c++
				Row = append(Row, []string{
					h,
					alignKey(fmt.Sprintf("sebak-error-%d", int(errorType))),
					alignValue(
						fmt.Sprintf(
							"%d | % 10s",
							errorCount,
							fmt.Sprintf(
								"%.5f％",
								float64(errorCount)/float64(countSEBAKError)*100,
							),
						),
					),
				})
			}
		}
	}
	table.SetAutoMergeCells(true)
	table.SetRowLine(true)
	table.AppendBulk(Row)
	table.Render()

	os.Exit(0)
}
