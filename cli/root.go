package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jhump/protoreflect/desc/protoparse"
	"github.com/spf13/cobra"
	"github.com/streamingfast/substreams/client"
	"github.com/streamingfast/substreams/decode"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreams "github.com/streamingfast/substreams/pb/sf/substreams/v1"
	"google.golang.org/grpc/metadata"
)

func init() {
	rootCmd.Flags().StringP("substreams-endpoint", "e", "api.streamingfast.io:443", "Substreams gRPC endpoint")
	rootCmd.Flags().String("substreams-api-token-envvar", "SUBSTREAMS_API_TOKEN", "name of variable containing Substreams Authentication token (JWT)")
	rootCmd.Flags().Int64P("start-block", "s", -1, "Start block for blockchain firehose")
	rootCmd.Flags().Uint64P("stop-block", "t", 0, "Stop block for blockchain firehose")
	rootCmd.Flags().StringP("proto-path", "I", "./proto", "Path of proto files")

	rootCmd.Flags().BoolP("insecure", "k", false, "Skip certificate validation on GRPC connection")
	rootCmd.Flags().BoolP("plaintext", "p", false, "Establish GRPC connection in plaintext")

	rootCmd.Flags().Bool("partial-mode", false, "Request partial processing mode (internal deployments only)")
	rootCmd.Flags().Bool("no-return-handler", false, "Avoid printing output for module")
}

// rootCmd represents the command to run substreams remotely
var rootCmd = &cobra.Command{
	Use:          "substreams [manifest] [module_name]",
	Short:        "Run substreams remotely",
	RunE:         run,
	Args:         cobra.ExactArgs(2),
	SilenceUsage: true,
}

func run(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	manifestPath := args[0]
	manif, err := manifest.New(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest %q: %w", manifestPath, err)
	}

	outputStreamNames := strings.Split(args[1], ",")

	protoIncludePath := mustGetString(cmd, "proto-path")
	protoFiles, err := filepath.Glob(filepath.Join(protoIncludePath, "*.proto"))
	parser := protoparse.Parser{}
	fileDescs, err := parser.ParseFiles(protoFiles...)
	if err != nil {
		return fmt.Errorf("error parsing proto files %q: %w", protoFiles, err)
	}

	returnHandler := func(any *pbsubstreams.BlockScopedData) error { return nil }
	if !mustGetBool(cmd, "no-return-handler") {

		returnHandler = decode.NewPrintReturnHandler(manif, fileDescs, outputStreamNames)
	}

	manif.PrintMermaid()

	manifProto, err := manif.ToProto()
	if err != nil {
		return fmt.Errorf("parse manifest to proto %q: %w", manifestPath, err)
	}

	graph, err := manifest.NewModuleGraph(manifProto.Modules)
	if err != nil {
		return fmt.Errorf("create module graph %w", err)
	}

	startBlock := mustGetInt64(cmd, "start-block")
	if startBlock == 0 {
		sb, err := graph.ModuleStartBlock(outputStreamNames[0])
		if err != nil {
			return fmt.Errorf("getting module start block: %w", err)
		}
		startBlock = int64(sb)
	}

	ssClient, callOpts, err := client.NewSubstreamsClient(
		mustGetString(cmd, "substreams-endpoint"),
		os.Getenv(mustGetString(cmd, "substreams-api-token-envvar")),
		mustGetBool(cmd, "insecure"),
		mustGetBool(cmd, "plaintext"),
	)
	if err != nil {
		return fmt.Errorf("substreams client setup: %w", err)
	}

	req := &pbsubstreams.Request{
		StartBlockNum: startBlock,
		StopBlockNum:  mustGetUint64(cmd, "stop-block"),
		ForkSteps:     []pbsubstreams.ForkStep{pbsubstreams.ForkStep_STEP_IRREVERSIBLE},
		Manifest:      manifProto,
		OutputModules: outputStreamNames,
	}

	if mustGetBool(cmd, "partial-mode") {
		ctx = metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"substreams-partial-mode": "true"}))
	}

	zlog.Info("connecting...")
	cli, err := ssClient.Blocks(ctx, req, callOpts...)
	if err != nil {
		return fmt.Errorf("call sf.substreams.v1.Stream/Blocks: %w", err)
	}

	zlog.Info("connected")
	for {
		resp, err := cli.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		switch r := resp.Message.(type) {
		case *pbsubstreams.Response_Progress:
			_ = r.Progress
		case *pbsubstreams.Response_SnapshotData:
			_ = r.SnapshotData
		case *pbsubstreams.Response_SnapshotComplete:
			_ = r.SnapshotComplete
		case *pbsubstreams.Response_Data:
			if err := returnHandler(r.Data); err != nil {
				fmt.Printf("RETURN HANDLER ERROR: %s\n", err)
			}
		}
	}

}
