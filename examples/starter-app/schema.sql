CREATE TABLE users (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  email VARCHAR(255) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY users_email (email)
);

CREATE TABLE orders (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  user_id BIGINT NOT NULL,
  total DECIMAL(20, 2) NOT NULL,
  PRIMARY KEY (id),
  KEY orders_user_id_id (user_id, id)
);

CREATE TABLE roles (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  name VARCHAR(255) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY roles_name (name)
);

CREATE TABLE user_roles (
  user_id BIGINT NOT NULL,
  role_id BIGINT NOT NULL,
  PRIMARY KEY (user_id, role_id),
  KEY user_roles_role_id_user_id (role_id, user_id)
);

CREATE TABLE clips (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  title VARCHAR(255) NOT NULL,
  PRIMARY KEY (id)
);

CREATE TABLE clip_genres (
  clip_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  PRIMARY KEY (clip_id, genre_id),
  KEY clip_genres_genre_id_clip_id (genre_id, clip_id)
);

CREATE TABLE job_leases (
  job_id BIGINT NOT NULL,
  lock_owner VARCHAR(255) NULL,
  lock_until DATETIME(6) NULL,
  retry_count BIGINT NOT NULL,
  last_error TEXT NULL,
  PRIMARY KEY (job_id)
);

CREATE TABLE videos (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  title VARCHAR(255) NOT NULL,
  deleted_at DATETIME(6) NULL,
  PRIMARY KEY (id)
);

CREATE TABLE user_watch_later_videos (
  user_id BIGINT NOT NULL,
  video_id BIGINT NOT NULL,
  PRIMARY KEY (user_id, video_id),
  KEY user_watch_later_videos_video_id_user_id (video_id, user_id)
);
