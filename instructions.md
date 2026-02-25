# Minerva - AI Curator of Knowledge

Minerva is name for this program and it's role is seek new information and offer curated selection of books and perhaps later other interesting medium.

We have a choice of using node.js or Go - and we should consider which approach will scale better for more content and features which will be added later.

We are using FreshRSS for first source of information, from there were fetch favourite rss items as user has marked those to be interesting.

FreshRSS api isnt very well done, but from there we first fetch a list of favourites and then fetch all rss items which are filtered by saved status.

Since rss items often contains only fraction of content, we need to fetch real arcticle and process that in manner that Ollama can read it's content, so we need to strip html tags, special characters, etc so that Ollama payload will be valid JSON. Should we use existing tool here is good question.

This data is then feed to Ollama to be summerized, keyworded and figuring insight from article data.

This data with original link to arcticle we are storing, we can use SQlite for first attemps.

This data is them used to query possible suitable books via searXNG, using metadata what LLM has created.

For future we are going to add Koha support here too and notifications via ntfy.

So, this project relies heavily on http requests and parsing complex data strcutures from html pages, we can consider using LLM for this too if applicaple vs regex style approach.

project structure should separate concerns and be modular as there are more to come.

I have Nomad based orchestration which I would like to use, in that sense Go might be better, but please do dissagree if you have a better idea and better tooling exist elsewere.