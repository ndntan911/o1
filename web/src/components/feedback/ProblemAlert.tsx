import type { ApiProblem } from '@/lib/api/problems';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { cn } from '@/lib/cn';

export interface ProblemAlertProps {
	problem?: ApiProblem | null;
	className?: string;
}

export function ProblemAlert({ problem, className }: ProblemAlertProps) {
	if (!problem) return null;

	return (
		<Alert variant="destructive" className={cn(className)}>
			<AlertTitle>{problem.title}</AlertTitle>
			{problem.detail ? (
				<AlertDescription>{problem.detail}</AlertDescription>
			) : null}
			<p className="font-mono text-xs text-muted-foreground">
				Code: {problem.code}
			</p>
		</Alert>
	);
}
