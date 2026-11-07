import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { MessageSquare, Send, Webhook } from 'lucide-react';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Switch } from '@/components/ui/switch';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { AlertChannelKind, Workspace } from '@/lib/api/types';
import { canManageAlertChannels } from '@/lib/roles';
import { useMonitorAlertChannels, useSetMonitorChannel } from './hooks';

type ChannelKind = Exclude<AlertChannelKind, 'email'>;

const KIND_META: Record<ChannelKind, { label: string; Icon: typeof Webhook }> =
	{
		webhook: { label: 'Webhook', Icon: Webhook },
		slack: { label: 'Slack', Icon: MessageSquare },
		telegram: { label: 'Telegram', Icon: Send },
	};

export interface MonitorAlertChannelsProps {
	workspace: Workspace;
	monitorId: string;
	canManage: boolean;
}

export function MonitorAlertChannels({
	workspace,
	monitorId,
	canManage,
}: MonitorAlertChannelsProps) {
	if (!canManage) return null;

	return (
		<MonitorAlertChannelsContent workspace={workspace} monitorId={monitorId} />
	);
}

function MonitorAlertChannelsContent({
	workspace,
	monitorId,
}: Omit<MonitorAlertChannelsProps, 'canManage'>) {
	const query = useMonitorAlertChannels(workspace.id, monitorId);
	const update = useSetMonitorChannel(workspace.id, monitorId);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const channels = query.data?.channels ?? [];

	return (
		<section
			aria-label="Alert channels"
			className="space-y-3 rounded-xl border p-4"
		>
			<div>
				<h2 className="font-heading text-lg font-medium">Alert channels</h2>
				<p className="text-sm text-muted-foreground">
					Choose which channels send notifications for this monitor.
				</p>
			</div>

			{query.isError ? (
				<ProblemAlert problem={problemFromError(query.error)} />
			) : null}
			{problem ? <ProblemAlert problem={problem} /> : null}

			{query.isPending ? (
				<p className="text-sm text-muted-foreground" role="status">
					Loading alert channels…
				</p>
			) : channels.length === 0 ? (
				<p className="text-sm text-muted-foreground">
					No alert channels are configured for this workspace.{' '}
					{canManageAlertChannels(workspace.role) ? (
						<Link
							to="/w/$workspaceSlug/settings/alerts"
							params={{ workspaceSlug: workspace.slug }}
							className="font-medium underline underline-offset-4"
						>
							Configure alert channels
						</Link>
					) : null}
				</p>
			) : (
				<ul className="divide-y">
					{channels.map((channel) => {
						const meta =
							channel.kind === 'email'
								? null
								: KIND_META[channel.kind as ChannelKind];
						const Icon = meta?.Icon;
						const pending =
							update.isPending && update.variables?.channelId === channel.id;
						return (
							<li
								key={channel.id}
								className="flex flex-wrap items-center justify-between gap-3 py-2"
							>
								<div className="min-w-0 space-y-0.5">
									<div className="flex items-center gap-2">
										{Icon ? (
											<Icon
												aria-hidden="true"
												className="size-4 text-muted-foreground"
											/>
										) : null}
										<span className="text-sm font-medium">{channel.name}</span>
										{meta ? (
											<span className="text-xs text-muted-foreground">
												{meta.label}
											</span>
										) : null}
									</div>
									{!channel.channel_enabled ? (
										<p className="text-xs text-muted-foreground">
											Disabled for this workspace
										</p>
									) : null}
								</div>
								<Switch
									aria-label={`${channel.name} alerts`}
									checked={channel.enabled}
									disabled={!channel.channel_enabled || pending}
									onCheckedChange={(enabled) => {
										setProblem(null);
										update.mutate(
											{ channelId: channel.id, enabled },
											{
												onError: (error) => setProblem(problemFromError(error)),
											},
										);
									}}
								/>
							</li>
						);
					})}
				</ul>
			)}
		</section>
	);
}
