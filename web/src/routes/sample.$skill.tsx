import { createFileRoute } from '@tanstack/react-router';
import { Sample } from '../pages/samples';

export const Route = createFileRoute('/sample/$skill')({
  component: () => <Sample skill={Route.useParams().skill} />,
});
