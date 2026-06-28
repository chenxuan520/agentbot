package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/backendfactory"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/failurenotify"
	"github.com/chenxuan520/agentbot/internal/flow"
	feishugateway "github.com/chenxuan520/agentbot/internal/gateway/feishu"
	"github.com/chenxuan520/agentbot/internal/localapi"
	"github.com/chenxuan520/agentbot/internal/observability"
	"github.com/chenxuan520/agentbot/internal/progress"
	"github.com/chenxuan520/agentbot/internal/provider"
	"github.com/chenxuan520/agentbot/internal/remoteagent"
	"github.com/chenxuan520/agentbot/internal/scheduler"
	"github.com/chenxuan520/agentbot/internal/session"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

const feishuListenerRetryDelay = 5 * time.Second

type feishuListener interface {
	Listen(ctx context.Context) error
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agent-bot <run|workspace|session|schedule|control|provider|backend> ...")
	}

	cfg, err := config.Load(".")
	if err != nil {
		return err
	}
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	workspaceManager := workspace.NewManager(cfg, store)
	sessionService := session.NewService(store, workspaceManager)
	progressService := progress.NewService(store)
	schedulerService := scheduler.NewService(store)
	controlService := control.NewService(store)
	accessTokenService := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	flowService := flow.NewService(cfg, sessionService, controlService, accessTokenService)
	remoteAgentHub := remoteagent.NewHub(flowService.SendAgentMessageToProvider)
	remoteAgentHub.SetAckClearer(flowService.DeleteReactionForProvider)
	flowService.SetRemoteRouter(remoteAgentHub)
	schedulerRunner := scheduler.NewRunner(schedulerService, scheduler.NewMultiHandler(
		scheduler.NewFileHandler(store),
		scheduler.NewPromptHandler(cfg, flowService),
		scheduler.NewNotifyHandler(cfg, flowService),
	))
	localAPIServer := localapi.New(cfg, controlService, schedulerService, sessionService, flowService, accessTokenService)
	localAPIServer.SetRemoteAgentServer(remoteAgentHub)

	switch args[0] {
	case "workspace":
		return runWorkspaceAction(workspaceManager, args[1:])
	case "session":
		return runSessionAction(cfg, sessionService, flowService, args[1:])
	case "schedule":
		return runScheduleAction(cfg, schedulerService, schedulerRunner, args[1:])
	case "control":
		return runControlAction(controlService, args[1:])
	case "provider":
		return runProviderAction(cfg, sessionService, flowService, progressService, localAPIServer, args[1:])
	case "run":
		return runDaemon(cfg, sessionService, flowService, progressService, schedulerRunner, localAPIServer, args[1:])
	case "backend":
		return runBackendAction(cfg, args[1:])
	default:
		return fmt.Errorf("unknown command group %q", args[0])
	}
}

func runWorkspaceAction(manager *workspace.Manager, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agent-bot workspace <ensure|rebuild> [provider conversation-id]")
	}

	ref, _, err := resolveRef(args, 1)
	if err != nil {
		return fmt.Errorf("usage: agent-bot workspace <ensure|rebuild> [provider conversation-id]")
	}

	var result any

	switch args[0] {
	case "ensure":
		result, err = manager.Ensure(ref)
	case "rebuild":
		result, err = manager.Rebuild(ref)
	default:
		return fmt.Errorf("unknown workspace action %q", args[0])
	}
	if err != nil {
		return err
	}

	return printJSON(result)
}

func runSessionAction(cfg config.Config, service *session.Service, flowService *flow.Service, args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("usage: agent-bot session <prepare|bind|clear|ensure-active|prompt> <provider> <conversation-id> ...")
	}

	ref := conversation.Ref{Provider: args[1], ConversationID: args[2]}

	switch args[0] {
	case "prepare":
		result, err := service.Prepare(ref, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	case "bind":
		if len(args) < 5 {
			return fmt.Errorf("usage: agent-bot session bind <provider> <conversation-id> <backend> <session-id>")
		}
		return service.Bind(ref, args[3], args[4], time.Now().UTC())
	case "clear":
		return service.ClearActive(ref)
	case "ensure-active":
		prepared, err := service.Prepare(ref, time.Now().UTC())
		if err != nil {
			return err
		}
		if !prepared.NeedNewSession {
			return printJSON(map[string]string{"sessionID": prepared.ActiveSessionID, "backend": prepared.AgentBackend})
		}
		client, err := backendfactory.FromSettings(cfg, prepared.Workspace.Settings)
		if err != nil {
			return err
		}
		sessionID, err := client.CreateSession(context.Background(), prepared.Workspace.Path)
		if err != nil {
			return err
		}
		if err := service.Bind(ref, prepared.AgentBackend, sessionID, time.Now().UTC()); err != nil {
			return err
		}
		return printJSON(map[string]string{"sessionID": sessionID, "backend": prepared.AgentBackend})
	case "prompt":
		if len(args) < 4 {
			return fmt.Errorf("usage: agent-bot session prompt <provider> <conversation-id> <text>")
		}
		result, err := flowService.PromptConversation(context.Background(), ref, args[3])
		if err != nil {
			return err
		}
		return printJSON(result)
	default:
		return fmt.Errorf("unknown session action %q", args[0])
	}
}

func runBackendAction(cfg config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agent-bot backend health [backend]")
	}

	switch args[0] {
	case "health":
		backendName := cfg.DefaultBackend
		if len(args) >= 2 && args[1] != "" {
			backendName = args[1]
		}
		client, err := backendfactory.FromConfig(cfg, backendName)
		if err != nil {
			return err
		}
		if err := client.Health(context.Background()); err != nil {
			return err
		}
		return printJSON(map[string]string{"backend": client.Name(), "status": "healthy"})
	default:
		return fmt.Errorf("unknown backend action %q", args[0])
	}
}

func runDaemon(cfg config.Config, sessionService *session.Service, flowService *flow.Service, progressService *progress.Service, schedulerRunner *scheduler.Runner, localAPIServer *localapi.Server, args []string) error {
	ctx := context.Background()
	cancel := func() {}
	if len(args) >= 1 {
		seconds, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("usage: agent-bot run [seconds]; provider is read from agent-bot.json runtime.defaultProvider")
		}
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	}
	defer cancel()
	failureNotifier := failurenotify.NewFeishuWebhook(cfg.FeishuFailureWebhookURL)
	schedulerRunner.SetLoopErrorNotifier(func(err error) {
		notifyFailure(failureNotifier, "scheduler-loop-error", buildFailureText("scheduler loop error", fmt.Sprintf("err=%v | action=continue", err)))
	})
	// Requeue jobs left running by a previous crash before the loop starts, so
	// orphaned scheduled jobs are not silently lost across restarts.
	if _, _, err := schedulerRunner.Recover(); err != nil {
		log.Printf("scheduler recover failed: %v", err)
	}

	switch cfg.DefaultProvider {
	case "feishu":
		if err := sessionService.SyncLegacyBotPollerState(); err != nil {
			return err
		}
		// The Feishu listener also runs the other-bot poller internally, so the
		// daemon only needs to start the listener, scheduler, and local API.
		listener := feishugateway.NewListener(cfg, flowService, progressService, sessionService)
		errCh := make(chan error, 3)
		go func() {
			errCh <- localAPIServer.Listen(ctx)
		}()
		go func() {
			interval := time.Duration(cfg.SchedulerIntervalSeconds) * time.Second
			errCh <- schedulerRunner.Loop(ctx, interval, cfg.SchedulerBatchLimit)
		}()
		go func() {
			errCh <- runFeishuListenerLoop(ctx, cfg, listener, failureNotifier)
		}()

		for i := 0; i < 3; i++ {
			if err := <-errCh; err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported runtime.defaultProvider %q", cfg.DefaultProvider)
	}
}

func runScheduleAction(cfg config.Config, service *scheduler.Service, runner *scheduler.Runner, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agent-bot schedule <create|create-cron|list|due|run-due|worker|running|complete|cancel> ...")
	}
	prompts := scheduler.NewPromptFileStore(cfg)

	switch args[0] {
	case "create":
		ref, next, err := resolveRef(args, 1)
		if err != nil {
			return err
		}
		if len(args) < next+2 {
			return fmt.Errorf("usage: agent-bot schedule create [provider conversation-id] <run-at-rfc3339> <route> [payload-json]")
		}
		payloadMap, err := parseSchedulePayloadArg(args, next+2)
		if err != nil {
			return err
		}
		if err := scheduler.ValidateMessageDeliveryPayload(payloadMap); err != nil {
			return err
		}
		runAt, err := scheduler.FirstRunAt(args[next], "", "", time.Now().UTC())
		if err != nil {
			return err
		}
		job, err := service.Schedule(
			ref,
			args[next+1],
			mustJSONString(payloadMap),
			runAt,
		)
		if err != nil {
			return err
		}
		return printJSON(job)
	case "create-cron":
		ref, next, err := resolveRef(args, 1)
		if err != nil {
			return err
		}
		if len(args) < next+3 {
			return fmt.Errorf("usage: agent-bot schedule create-cron [provider conversation-id] <cron-expression> <timezone> <route> [payload-json]")
		}
		payloadMap, err := parseSchedulePayloadArg(args, next+3)
		if err != nil {
			return err
		}
		payloadMap = scheduler.ApplyCronPayload(payloadMap, args[next], args[next+1])
		payloadMap, err = prompts.MaterializeRecurringPayload(ref, args[next+2], payloadMap)
		if err != nil {
			return err
		}
		if err := scheduler.ValidateMessageDeliveryPayload(payloadMap); err != nil {
			return err
		}
		runAt, err := scheduler.FirstRunAt("", args[next], args[next+1], time.Now().UTC())
		if err != nil {
			return err
		}
		job, err := service.Schedule(ref, args[next+2], mustJSONString(payloadMap), runAt)
		if err != nil {
			return err
		}
		return printJSON(job)
	case "list":
		ref, next, err := resolveRef(args, 1)
		if err != nil {
			return fmt.Errorf("usage: agent-bot schedule list [provider conversation-id] [--all] [limit]")
		}
		limit := 100
		includeDone := false
		for _, value := range args[next:] {
			if value == "--all" {
				includeDone = true
				continue
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			limit = parsed
		}
		var jobs []scheduler.Job
		if includeDone {
			jobs, err = service.List(ref, limit)
		} else {
			jobs, err = service.ListActive(ref, limit)
		}
		if err != nil {
			return err
		}
		return printJSON(jobs)
	case "due":
		limit := 50
		if len(args) >= 2 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			limit = parsed
		}
		jobs, err := service.Due(time.Now().UTC(), limit)
		if err != nil {
			return err
		}
		return printJSON(jobs)
	case "run-due":
		limit := 50
		if len(args) >= 2 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			limit = parsed
		}
		jobs, err := runner.RunDue(time.Now().UTC(), limit)
		if err != nil {
			return err
		}
		return printJSON(jobs)
	case "worker":
		pollSeconds := 1
		windowSeconds := 10
		limit := 50
		if len(args) >= 2 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			pollSeconds = parsed
		}
		if len(args) >= 3 {
			parsed, err := strconv.Atoi(args[2])
			if err != nil {
				return err
			}
			windowSeconds = parsed
		}
		if len(args) >= 4 {
			parsed, err := strconv.Atoi(args[3])
			if err != nil {
				return err
			}
			limit = parsed
		}
		deadline := time.Now().UTC().Add(time.Duration(windowSeconds) * time.Second)
		var triggered []scheduler.Job
		for {
			now := time.Now().UTC()
			jobs, err := runner.RunDue(now, limit)
			if err != nil {
				return err
			}
			if len(jobs) > 0 {
				triggered = append(triggered, jobs...)
			}
			if now.After(deadline) || now.Equal(deadline) {
				break
			}
			time.Sleep(time.Duration(pollSeconds) * time.Second)
		}
		return printJSON(triggered)
	case "running":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent-bot schedule running <job-id>")
		}
		return service.MarkRunning(args[1])
	case "complete":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent-bot schedule complete <job-id>")
		}
		return service.Complete(args[1])
	case "cancel":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent-bot schedule cancel <job-id>")
		}
		return service.Cancel(args[1])
	default:
		return fmt.Errorf("unknown schedule action %q", args[0])
	}
}

func parseSchedulePayloadArg(args []string, index int) (map[string]any, error) {
	if len(args) <= index {
		return nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(args[index]), &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func mustJSONString(value any) string {
	if value == nil {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func runControlAction(service *control.Service, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agent-bot control <refuse|active|cancel> ...")
	}

	switch args[0] {
	case "refuse":
		ref, next, err := resolveRef(args, 1)
		if err != nil {
			return err
		}
		if len(args) < next+2 {
			return fmt.Errorf("usage: agent-bot control refuse [provider conversation-id] <until-rfc3339> <reason>")
		}
		until, err := time.Parse(time.RFC3339, args[next])
		if err != nil {
			return err
		}
		rule, err := service.Refuse(ref, until, args[next+1])
		if err != nil {
			return err
		}
		return printJSON(rule)
	case "active":
		ref, _, err := resolveRef(args, 1)
		if err != nil {
			return err
		}
		rules, err := service.Active(ref, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(rules)
	case "cancel":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent-bot control cancel <rule-id>")
		}
		return service.Cancel(args[1])
	default:
		return fmt.Errorf("unknown control action %q", args[0])
	}
}

func runProviderAction(cfg config.Config, sessionService *session.Service, flowService *flow.Service, progressService *progress.Service, localAPIServer *localapi.Server, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: agent-bot provider <provider> <health|process-text|listen> ...")
	}

	client, err := provider.FromConfig(cfg, args[0])
	if err != nil {
		return err
	}

	switch args[1] {
	case "health":
		if err := client.Health(context.Background()); err != nil {
			return err
		}
		return printJSON(map[string]string{"provider": client.Name(), "status": "healthy"})
	case "process-text":
		if len(args) < 6 {
			return fmt.Errorf("usage: agent-bot provider <provider> process-text <conversation-id> <conversation-type> <message-id> <text>")
		}
		result, err := flowService.ProcessText(context.Background(), flow.TextInput{
			Provider:         args[0],
			ConversationID:   args[2],
			ConversationType: args[3],
			MessageID:        args[4],
			Text:             args[5],
			AddReaction:      args[4] != "",
		})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "listen":
		if args[0] != "feishu" {
			return fmt.Errorf("provider %q does not support listen", args[0])
		}
		ctx := context.Background()
		cancel := func() {}
		if len(args) >= 3 {
			seconds, err := strconv.Atoi(args[2])
			if err != nil {
				return err
			}
			ctx, cancel = context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
		}
		defer cancel()
		failureNotifier := failurenotify.NewFeishuWebhook(cfg.FeishuFailureWebhookURL)
		if err := sessionService.SyncLegacyBotPollerState(); err != nil {
			return err
		}
		// The Feishu listener runs the other-bot poller internally.
		listener := feishugateway.NewListener(cfg, flowService, progressService, sessionService)
		errCh := make(chan error, 2)
		go func() {
			errCh <- localAPIServer.Listen(ctx)
		}()
		go func() {
			errCh <- runFeishuListenerLoop(ctx, cfg, listener, failureNotifier)
		}()
		for i := 0; i < 2; i++ {
			if err := <-errCh; err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown provider action %q", args[1])
	}
}

func runFeishuListenerLoop(ctx context.Context, cfg config.Config, listener feishuListener, notifier *failurenotify.FeishuWebhook) error {
	return runFeishuListenerLoopWithRetry(ctx, cfg, listener, feishuListenerRetryDelay, notifier)
}

func runFeishuListenerLoopWithRetry(ctx context.Context, cfg config.Config, listener feishuListener, retryDelay time.Duration, notifier *failurenotify.FeishuWebhook) error {
	if cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" {
		return errors.New("missing feishu app credentials")
	}
	if retryDelay <= 0 {
		retryDelay = feishuListenerRetryDelay
	}

	for {
		err := listener.Listen(ctx)
		if err == nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("feishu listener stopped without error, retrying in %s", retryDelay)
			notifyFailure(notifier, "feishu-listener-stopped", buildFailureText("feishu listener stopped", fmt.Sprintf("retry_in=%s | action=retry", retryDelay)))
		} else {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return nil
			}
			log.Printf("feishu listener error: %v; retrying in %s", err, retryDelay)
			notifyFailure(notifier, "feishu-listener-error", buildFailureText("feishu listener error", fmt.Sprintf("err=%v | retry_in=%s | action=retry", err, retryDelay)))
		}

		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func failureCategory(key string) string {
	switch {
	case strings.Contains(key, "scheduler"):
		return "scheduler"
	case strings.Contains(key, "listener"), strings.Contains(key, "feishu"):
		return "listener"
	default:
		return "daemon"
	}
}

func buildFailureText(kind, detail string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("agent-bot %s | host=%s | detail=%s | time=%s", kind, host, detail, time.Now().UTC().Format(time.RFC3339))
}

func notifyFailure(notifier *failurenotify.FeishuWebhook, key, text string) {
	// Record first, unconditionally: the admin observability panel must capture
	// the failure even when no failure webhook is configured (no silent drops).
	observability.Record(observability.Event{
		Severity: observability.SeverityError,
		Category: failureCategory(key),
		Summary:  key,
		Detail:   text,
	})
	if notifier == nil || !notifier.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notifier.NotifyTextThrottled(ctx, key, text, 5*time.Minute); err != nil {
		log.Printf("failure webhook send failed: %v", err)
	}
}

func resolveRef(args []string, start int) (conversation.Ref, int, error) {
	if ref, ok := defaultRef(); ok {
		return ref, start, nil
	}
	if len(args) >= start+2 {
		return conversation.Ref{Provider: args[start], ConversationID: args[start+1]}, start + 2, nil
	}

	return conversation.Ref{}, 0, fmt.Errorf("missing provider/conversation context")
}

func defaultRef() (conversation.Ref, bool) {
	provider := os.Getenv("AGENT_BOT_PROVIDER")
	conversationID := os.Getenv("AGENT_BOT_CONVERSATION_ID")
	if provider == "" || conversationID == "" {
		return conversation.Ref{}, false
	}
	return conversation.Ref{Provider: provider, ConversationID: conversationID}, true
}

func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(data, '\n'))
	return err
}
