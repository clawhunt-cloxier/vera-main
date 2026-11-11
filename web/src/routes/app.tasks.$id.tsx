import { createFileRoute } from '@tanstack/react-router';
import { TaskDetail } from '../pages/task';

export const Route = createFileRoute('/app/tasks/$id')({
  component: () => <TaskDetail id={Route.useParams().id} />,
});
