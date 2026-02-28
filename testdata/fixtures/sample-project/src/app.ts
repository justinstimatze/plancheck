import { router } from './router.ts';

const app = {
  start() {
    console.log('app started');
    router.init();
  }
};

app.start();
