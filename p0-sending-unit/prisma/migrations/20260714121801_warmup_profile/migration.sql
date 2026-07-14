-- CreateTable
CREATE TABLE "WarmupProfile" (
    "accountId" TEXT NOT NULL,
    "lane" TEXT NOT NULL,
    "stage" TEXT NOT NULL,
    "warmupMessagesSent" INTEGER NOT NULL DEFAULT 0,
    "repliesReceived" INTEGER NOT NULL DEFAULT 0,
    "onlineSince" TEXT,
    "maturedAt" TEXT,
    "sentToday" INTEGER NOT NULL DEFAULT 0,
    "sentTodayDate" TEXT,

    CONSTRAINT "WarmupProfile_pkey" PRIMARY KEY ("accountId")
);
