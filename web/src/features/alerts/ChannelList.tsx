import { useState } from 'react';
import { MessageSquare, Send, Webhook } from 'lucide-react';
import { toast } from 'sonner';
import { ConfirmDialog } from '@/components/feedback/ConfirmDialog';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { AlertChannel, AlertChannelKind } from '@/lib/api/types';
import { ChannelDialog } from './ChannelDialog';
import {
	useDeleteAlertChannel,
	useTestAlertChannel,
	useUpdateAlertChannel,
} from './hooks';

type ChannelKind = Exclude<AlertChannelKind, 'email'>;

const KIND_META: Record<ChannelKind, { label: string; Icon: typeof Webhook }> =
	{
		webhook: { label: 'Webhook', Icon: Webhook },
		slack: { label: 'Slack', Icon: MessageSquare },
		telegram: { label: 'Telegram', Icon: Send },
	};

export interface ChannelListProps {
	workspaceId: string;
	channels: AlertChannel[];
}

export function ChannelList({ workspaceId, channels }: ChannelListProps) {
	const [dialog, setDialog] = useState<{ channel: AlertChannel | null } | null>(
		null,
	);
	const [deleteTarget, setDeleteTarget] = useState<AlertChannel | null>(null);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const update = useUpdateAlertChannel(workspaceId);
	const remove = useDeleteAlertChannel(workspaceId);
	const test = useTestAlertChannel(workspaceId);

	const rows = channels.filter(
		(channel): channel is AlertChannel & { kind: ChannelKind } =>
			channel.kind !== 'email',
	);

	return (
		<section className="space-y-4">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h2 className="font-heading text-lg font-medium">Other channels</h2>
					<p className="text-sm text-muted-foreground">
						Send incident notifications to webhooks, Slack, or Telegram.
					</p>
				</div>
				<Button
					type="button"
					variant="outline"
					onClick={() => setDialog({ channel: null })}
				>
					Add channel
				</Button>
			</div>

			{problem ? <ProblemAlert problem={problem} /> : null}

			{rows.length === 0 ? (
				<p className="text-sm text-muted-foreground">No other channels yet.</p>
			) : (
				<ul className="divide-y rounded-xl border">
					{rows.map((channel) => {
						const meta = KIND_META[channel.kind];
						const Icon = meta.Icon;
						const target = channel.url_preview ?? channel.chat_id ?? '';
						const pending =
							update.isPending && update.variables?.channelId === channel.id;
						return (
							<li
								key={channel.id}
								className="flex flex-wrap items-center gap-3 p-4"
							>
								<Icon
									aria-hidden="true"
									className="size-4 text-muted-foreground"
								/>
								<div className="min-w-0 flex-1 space-y-0.5">
									<div className="flex items-center gap-2">
										<span className="text-xs font-medium text-muted-foreground">
											{meta.label}
										</span>
										<span className="truncate text-sm font-medium">
											{channel.name}
										</span>
									</div>
									<p className="truncate text-xs text-muted-foreground">
										{target}
									</p>
								</div>
								<Switch
									aria-label={`${channel.name} enabled`}
									checked={channel.enabled}
									disabled={pending}
									onCheckedChange={(enabled) => {
										setProblem(null);
										update.mutate(
											{ channelId: channel.id, input: { enabled } },
											{
												onError: (error) => setProblem(problemFromError(error)),
											},
										);
									}}
								/>
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={() => setDialog({ channel })}
								>
									Edit
								</Button>
								<Button
									type="button"
									variant="outline"
									size="sm"
									aria-label={`Test ${channel.name}`}
									disabled={test.isPending}
									onClick={() => {
										setProblem(null);
										test.mutate(channel.id, {
											onSuccess: () => toast.success('Test notification sent'),
											onError: (error) => setProblem(problemFromError(error)),
										});
									}}
								>
									Test
								</Button>
								<Button
									type="button"
									variant="outline"
									size="sm"
									aria-label={`Delete ${channel.name}`}
									onClick={() => setDeleteTarget(channel)}
								>
									Delete
								</Button>
							</li>
						);
					})}
				</ul>
			)}

			{dialog ? (
				<ChannelDialog
					workspaceId={workspaceId}
					channel={dialog.channel}
					onClose={() => setDialog(null)}
				/>
			) : null}

			<ConfirmDialog
				open={deleteTarget !== null}
				title="Delete alert channel?"
				description={
					deleteTarget
						? `${deleteTarget.name} will stop sending new notifications. Delivery history is retained.`
						: undefined
				}
				confirmLabel="Delete channel"
				pending={remove.isPending}
				problem={problem}
				onConfirm={() => {
					if (!deleteTarget) return;
					remove.mutate(deleteTarget.id, {
						onSuccess: () => {
							toast.success('Alert channel deleted');
							setDeleteTarget(null);
							setProblem(null);
						},
						onError: (error) => setProblem(problemFromError(error)),
					});
				}}
				onOpenChange={(open) => {
					if (!open) {
						setDeleteTarget(null);
						setProblem(null);
					}
				}}
			/>
		</section>
	);
}
