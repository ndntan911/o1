import { useState, type FormEvent } from 'react';
import { toast } from 'sonner';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { AlertChannel, AlertChannelKind } from '@/lib/api/types';
import { useCreateAlertChannel, useUpdateAlertChannel } from './hooks';
import type { CreateAlertChannelInput, UpdateAlertChannelInput } from './api';

const KINDS: Array<{
	value: Exclude<AlertChannelKind, 'email'>;
	label: string;
}> = [
	{ value: 'webhook', label: 'Webhook' },
	{ value: 'slack', label: 'Slack' },
	{ value: 'telegram', label: 'Telegram' },
];

export interface ChannelDialogProps {
	workspaceId: string;
	channel: AlertChannel | null;
	onClose: () => void;
}

type ChannelKind = Exclude<AlertChannelKind, 'email'>;

function initialKind(channel: AlertChannel | null): ChannelKind {
	if (channel && channel.kind !== 'email') return channel.kind;
	return 'webhook';
}

export function ChannelDialog({
	workspaceId,
	channel,
	onClose,
}: ChannelDialogProps) {
	const editing = channel !== null;
	const [kind, setKind] = useState<ChannelKind>(initialKind(channel));
	const [name, setName] = useState(channel?.name ?? '');
	const [enabled, setEnabled] = useState(channel?.enabled ?? true);
	const [url, setUrl] = useState('');
	const [signingSecret, setSigningSecret] = useState('');
	const [clearSigningSecret, setClearSigningSecret] = useState(false);
	const [botToken, setBotToken] = useState('');
	const [chatId, setChatId] = useState(channel?.chat_id ?? '');
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const create = useCreateAlertChannel(workspaceId);
	const update = useUpdateAlertChannel(workspaceId);
	const pending = create.isPending || update.isPending;

	function handleSubmit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setProblem(null);

		const onError = (error: unknown) => setProblem(problemFromError(error));
		const onSuccess = () => {
			toast.success('Alert channel saved');
			onClose();
		};

		if (!editing) {
			const input: CreateAlertChannelInput = {
				kind,
				name: name.trim(),
				enabled,
			};
			if (kind === 'telegram') {
				input.bot_token = botToken;
				input.chat_id = chatId.trim();
			} else {
				input.url = url.trim();
				if (kind === 'webhook' && signingSecret) {
					input.signing_secret = signingSecret;
				}
			}
			create.mutate(input, { onSuccess, onError });
			return;
		}

		const input: UpdateAlertChannelInput = {
			name: name.trim(),
			enabled,
		};
		if (kind === 'telegram') {
			if (botToken) input.bot_token = botToken;
			input.chat_id = chatId.trim();
		} else if (url.trim()) {
			input.url = url.trim();
		}
		if (kind === 'webhook') {
			if (signingSecret) input.signing_secret = signingSecret;
			if (clearSigningSecret) input.clear_signing_secret = true;
		}
		update.mutate({ channelId: channel.id, input }, { onSuccess, onError });
	}

	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>
						{editing ? 'Edit alert channel' : 'Add alert channel'}
					</DialogTitle>
					<DialogDescription>
						Incident notifications are delivered for every monitor this channel
						is enabled on.
					</DialogDescription>
				</DialogHeader>

				{problem ? <ProblemAlert problem={problem} /> : null}

				<form className="space-y-4" onSubmit={handleSubmit}>
					{!editing ? (
						<div className="space-y-2">
							<Label htmlFor="channel-kind">Kind</Label>
							<Select
								value={kind}
								onValueChange={(value) => setKind(value as ChannelKind)}
							>
								<SelectTrigger
									id="channel-kind"
									aria-label="Kind"
									className="w-full"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{KINDS.map((option) => (
										<SelectItem key={option.value} value={option.value}>
											{option.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					) : null}

					<div className="space-y-2">
						<Label htmlFor="channel-name">Name</Label>
						<Input
							id="channel-name"
							value={name}
							onChange={(event) => setName(event.target.value)}
							required
						/>
					</div>

					<div className="flex items-center justify-between gap-4 rounded-lg border p-3">
						<Label htmlFor="channel-enabled">Enabled</Label>
						<Switch
							id="channel-enabled"
							aria-label="Channel enabled"
							checked={enabled}
							onCheckedChange={setEnabled}
						/>
					</div>

					{kind === 'webhook' || kind === 'slack' ? (
						<div className="space-y-2">
							<Label htmlFor="channel-url">
								{kind === 'webhook' ? 'Webhook URL' : 'Slack webhook URL'}
							</Label>
							<Input
								id="channel-url"
								type="url"
								value={url}
								onChange={(event) => setUrl(event.target.value)}
								placeholder={
									editing && channel?.url_preview
										? channel.url_preview
										: undefined
								}
								required={!editing}
							/>
							{editing && channel?.url_preview ? (
								<p className="text-xs text-muted-foreground">
									Configured — leave blank to keep
								</p>
							) : null}
						</div>
					) : null}

					{kind === 'webhook' ? (
						<div className="space-y-2">
							<Label htmlFor="channel-signing-secret">Signing secret</Label>
							<Input
								id="channel-signing-secret"
								type="password"
								value={signingSecret}
								onChange={(event) => setSigningSecret(event.target.value)}
							/>
							{editing && channel?.signing_secret_set ? (
								<p className="text-xs text-muted-foreground">
									Signing secret configured — leave blank to keep
								</p>
							) : null}
							{editing && channel?.signing_secret_set ? (
								<label className="flex items-center gap-2 text-sm">
									<input
										type="checkbox"
										checked={clearSigningSecret}
										onChange={(event) =>
											setClearSigningSecret(event.target.checked)
										}
									/>
									Remove signing secret
								</label>
							) : null}
						</div>
					) : null}

					{kind === 'telegram' ? (
						<>
							<div className="space-y-2">
								<Label htmlFor="channel-bot-token">Bot token</Label>
								<Input
									id="channel-bot-token"
									type="password"
									value={botToken}
									onChange={(event) => setBotToken(event.target.value)}
									required={!editing}
								/>
								{editing && channel?.bot_token_set ? (
									<p className="text-xs text-muted-foreground">
										Configured — leave blank to keep
									</p>
								) : null}
							</div>
							<div className="space-y-2">
								<Label htmlFor="channel-chat-id">Chat ID</Label>
								<Input
									id="channel-chat-id"
									value={chatId}
									onChange={(event) => setChatId(event.target.value)}
									required
								/>
							</div>
						</>
					) : null}

					<DialogFooter>
						<Button
							type="button"
							variant="outline"
							onClick={onClose}
							disabled={pending}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={pending}>
							{pending ? 'Saving…' : 'Save channel'}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
