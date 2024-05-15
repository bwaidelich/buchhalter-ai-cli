package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"buchhalter/lib/repository"
	"buchhalter/lib/utils"
)

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connects to the Buchhalter Platform and verifies your premium membership",
	Long:  "The connect command verifies your premium membership by logging into the Buchhalter Platform. This is required to use your premium membership.",
	Run:   RunConnectCommand,
}

func init() {
	rootCmd.AddCommand(connectCmd)
}

func RunConnectCommand(cmd *cobra.Command, cmdArgs []string) {
	// Init logging
	buchhalterDirectory := viper.GetString("buchhalter_directory")
	developmentMode := viper.GetBool("dev")
	logSetting, err := cmd.Flags().GetBool("log")
	if err != nil {
		fmt.Printf("Error reading log flag: %s\n", err)
		os.Exit(1)
	}
	logger, err := initializeLogger(logSetting, developmentMode, buchhalterDirectory)
	if err != nil {
		fmt.Printf("Error on initializing logging: %s\n", err)
		os.Exit(1)
	}
	logger.Info("Booting up", "development_mode", developmentMode)
	defer logger.Info("Shutting down")

	// Print welcome message
	s := fmt.Sprintf(
		"%s\n%s\n%s%s\n%s\n",
		headerStyle(LogoText),
		textStyle("Automatically sync all your incoming invoices from your suppliers. "),
		textStyle("More information at: "),
		textStyleBold("https://buchhalter.ai"),
		textStyleGrayBold(fmt.Sprintf("Using CLI %s", CliVersion)),
	)
	fmt.Println(s)
	fmt.Println(textStyle("Connecting to the Buchhalter Platform ..."))

	// Read text input from user (API key)
	logger.Info("Reading user input")
	apiToken := ""
	for {
		fmt.Print("Your buchhalter API-Token: ")
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			logger.Error("User input could not be read", "error", err)
			fmt.Println("An error occurred while reading your api token. Please try again", err)
		}
		apiToken = strings.TrimSuffix(input, "\n")
		if len(apiToken) > 0 {
			break
		}
	}

	// Making API call
	buchhalterConfigDirectory := viper.GetString("buchhalter_config_directory")
	apiHost := viper.GetString("buchhalter_api_host")
	buchhalterAPIClient, err := repository.NewBuchhalterAPIClient(logger, apiHost, buchhalterConfigDirectory, apiToken, CliVersion)
	if err != nil {
		logger.Error("Error initializing Buchhalter API client", "error", err)
		fmt.Printf("Error initializing Buchhalter API client: %s\n", err)
		os.Exit(1)
	}

	logger.Info("Making API call")
	cliSyncResponse, err := buchhalterAPIClient.GetAuthenticatedUser()
	fmt.Println("")
	if err != nil {
		logger.Error("GetAuthenticatedUser API call not successful input could not be read", "error", err)
		fmt.Println(textStyle("Connecting to the Buchhalter Platform ... unsuccessful"))
		fmt.Println(textStyle("Please check your API-Token at https://app.buchhalter.ai/token and try again."))
		return
	}

	fmt.Printf("Hi %s (%s), you are connected to the Buchhalter Platform.\n", cliSyncResponse.User.Name, cliSyncResponse.User.Email)
	fmt.Println("Your teams:")
	for _, team := range cliSyncResponse.User.Teams {
		fmt.Printf("  - %s\n", team.Name)
	}
	fmt.Println("")

	// Write file
	homeDir, _ := os.UserHomeDir()
	buchhalterConfigDir := filepath.Join(homeDir, ".buchhalter")
	apiTokenFile := filepath.Join(buchhalterConfigDir, ".buchhalter-api-token")
	logger.Info("Writing API token to file", "file", apiTokenFile)
	err = utils.WriteStringToFile(apiTokenFile, apiToken)
	if err != nil {
		logger.Error("API token could not be written to file", "error", err)
		fmt.Println(textStyle("Connecting to the Buchhalter Platform ... unsuccessful"))
		fmt.Println(textStyle("Token could not be written to disk. Please try again."))
		return
	}

	fmt.Println(textStyle("Connecting to the Buchhalter Platform ... successful"))
}
