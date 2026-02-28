import { usersHandler } from './handlers/users.ts';

export const router = {
  init() {
    this.register('/users', usersHandler);
  },
  register(path: string, handler: Function) {
    console.log(`registered: ${path}`);
  }
};
