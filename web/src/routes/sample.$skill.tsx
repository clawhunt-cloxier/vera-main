import { createFileRoute } from '@tanstack/react-router';
import { Sample } from '../pages/public';

export const Route = createFileRoute('/sample/$skill')({ component: () => <Sample skill={Route.useParams().skill} /> });
