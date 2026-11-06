import { useState } from 'react';
import { toast } from 'sonner';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { AlertChannel, Workspace } from '@/lib/api/types';
import { canManageAlertChannels } from '@/lib/roles';
import { useAlertChannels, useSaveEmailAlertChannel } from './hooks';
import { RecipientsField } from './RecipientsField';

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

function isRetryable(error: unknown): boolean {
	if (!isApiProblem(error)) return true;
	return error.problem.status === 0 || error.problem.status >= 500;
}

export interface AlertSettingsScreenProps {
	workspace: Workspace;
}

export function AlertSettingsScreen({ workspace }: AlertSettingsScreenProps) {
	if (!canManageAlertChannels(workspace.role)) {
		return (
			<section className="space-y-2">
				<h1 className="font-heading text-xl font-semibold">Alert settings</h1>
				<p className="text-sm text-muted-foreground">
					You do not have access to alert settings.
				</p>
			</section>
		);
	}

	return <AlertSettingsContent workspace={workspace} />;
}

function AlertSettingsContent({ workspace }: AlertSettingsScreenProps) {
	const channelsQuery = useAlertChannels(workspace.id);

	if (channelsQuery.isPending) return <PageSkeleton />;

	if (!channelsQuery.data) {
		return (
			<ErrorPanel
				title="Could not load alert settings"
				message={errorMessage(channelsQuery.error)}
				problem={problemFromError(channelsQuery.error)}
				onRetry={
					isRetryable(channelsQuery.error)
						? () => void channelsQuery.refetch()
						: undefined
				}
			/>
		);
	}

	const channel =
		channelsQuery.data.channels.find((item) => item.kind === 'email') ?? null;

	return (
		<AlertSettingsForm
			key={channel?.updated_at ?? 'new'}
			workspace={workspace}
			channel={channel}
		/>
	);
}

interface AlertSettingsFormProps {
	workspace: Workspace;
	channel: AlertChannel | null;
}

function AlertSettingsForm({ workspace, channel }: AlertSettingsFormProps) {
	const save = useSaveEmailAlertChannel(workspace.id);
	const [recipients, setRecipients] = useState<string[]>(
		channel?.recipients ?? [],
	);
	const [enabled, setEnabled] = useState(channel?.enabled ?? false);
	const [validation, setValidation] = useState<string | null>(null);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	function handleRecipientsChange(next: string[]) {
		setRecipients(next);
		setValidation(null);
	}

	function handleSave() {
		setProblem(null);
		if (enabled && recipients.length === 0) {
			setValidation('Add at least one recipient before enabling email alerts.');
			return;
		}

		setValidation(null);
		save.mutate(
			{ recipients, enabled },
			{
				onError: (error) => setProblem(problemFromError(error)),
				onSuccess: () => {
					setProblem(null);
					toast.success('Email alert settings saved');
				},
			},
		);
	}

	return (
		<section className="max-w-2xl space-y-6">
			<div>
				<h1 className="font-heading text-xl font-semibold">Alert settings</h1>
				<p className="text-sm text-muted-foreground">
					Choose who receives incident emails for {workspace.name}.
				</p>
			</div>

			{problem ? <ProblemAlert problem={problem} /> : null}

			<div className="flex flex-wrap items-center justify-between gap-4 rounded-xl border p-4">
				<div className="space-y-1">
					<Label htmlFor="email-alerts-enabled">Email alerts</Label>
					<p className="text-sm text-muted-foreground">
						Send an email when a monitor opens or resolves an incident.
					</p>
				</div>
				<Switch
					id="email-alerts-enabled"
					aria-label="Email alerts"
					checked={enabled}
					disabled={save.isPending}
					onCheckedChange={(checked) => {
						setEnabled(checked);
						setValidation(null);
					}}
				/>
			</div>

			<RecipientsField
				recipients={recipients}
				onChange={handleRecipientsChange}
				disabled={save.isPending}
			/>

			{validation ? (
				<p role="alert" className="text-sm text-destructive">
					{validation}
				</p>
			) : null}

			<div>
				<Button type="button" disabled={save.isPending} onClick={handleSave}>
					{save.isPending ? 'Saving…' : 'Save changes'}
				</Button>
			</div>
		</section>
	);
}
